// Package people keeps the household's people who are not signed-in adults,
// such as children or a parent the couple cares for. A person is a fact about
// the household; it never grants access. The two-adult user cap is unchanged.
package people

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/glnarayanan/mithra/internal/policy"
)

var (
	ErrInvalid  = errors.New("person details are incomplete or invalid")
	ErrNotFound = errors.New("person not found")
)

// Relationships lists the kinds of person Mithra keeps, in display order.
var Relationships = []string{"child", "parent", "adult", "other"}

type Person struct {
	ID, HouseholdID, OwnerID string
	Visibility               policy.Visibility
	Name, Relationship       string
	BirthDate                string
	Version                  int64
	CreatedAt, UpdatedAt     string
}

type Draft struct {
	Name, Relationship, BirthDate string
	Visibility                    policy.Visibility
}

type Service struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB) *Service { return &Service{db: db, now: time.Now} }

// WithClock returns a copy that reads time from now, for tests.
func (s *Service) WithClock(now func() time.Time) *Service {
	return &Service{db: s.db, now: now}
}

// List returns the people the actor can see, by name.
func (s *Service) List(ctx context.Context, actor policy.ActorScope) ([]Person, error) {
	if err := authorize(ctx, s.db, actor); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,household_id,owner_user_id,visibility,display_name,relationship,birth_date,version,created_at,updated_at FROM household_people WHERE household_id=? AND archived=0 AND (visibility='shared' OR owner_user_id=?) ORDER BY display_name COLLATE NOCASE, created_at`, actor.HouseholdID, actor.ActorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Person
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.ID, &p.HouseholdID, &p.OwnerID, &p.Visibility, &p.Name, &p.Relationship, &p.BirthDate, &p.Version, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// Get returns one visible person.
func (s *Service) Get(ctx context.Context, actor policy.ActorScope, id string) (Person, error) {
	people, err := s.List(ctx, actor)
	if err != nil {
		return Person{}, err
	}
	for _, p := range people {
		if p.ID == id {
			return p, nil
		}
	}
	return Person{}, ErrNotFound
}

// Create adds a person. People default to Shared, because a child or a parent
// is usually a fact both adults keep.
func (s *Service) Create(ctx context.Context, actor policy.ActorScope, draft Draft) (Person, error) {
	draft, err := clean(draft)
	if err != nil {
		return Person{}, err
	}
	id, err := newID()
	if err != nil {
		return Person{}, err
	}
	if err := authorize(ctx, s.db, actor); err != nil {
		return Person{}, err
	}
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO household_people(id,household_id,owner_user_id,visibility,display_name,relationship,birth_date,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,1,?,?)`, id, actor.HouseholdID, actor.ActorID, draft.Visibility, draft.Name, draft.Relationship, draft.BirthDate, stamp, stamp); err != nil {
		return Person{}, err
	}
	return Person{ID: id, HouseholdID: actor.HouseholdID, OwnerID: actor.ActorID, Visibility: draft.Visibility, Name: draft.Name, Relationship: draft.Relationship, BirthDate: draft.BirthDate, Version: 1, CreatedAt: stamp, UpdatedAt: stamp}, nil
}

// Update changes a person's details when version still matches.
func (s *Service) Update(ctx context.Context, actor policy.ActorScope, id string, version int64, draft Draft) (Person, error) {
	draft, err := clean(draft)
	if err != nil {
		return Person{}, err
	}
	current, err := s.Get(ctx, actor, id)
	if err != nil {
		return Person{}, err
	}
	if err := actor.CanMutate(policy.Resource{HouseholdID: current.HouseholdID, OwnerID: current.OwnerID, Visibility: current.Visibility, Version: current.Version}, version); err != nil {
		return Person{}, err
	}
	// Only the adult who added a private person can make them shared or keep them private.
	if current.Visibility == policy.Personal && current.OwnerID != actor.ActorID {
		return Person{}, policy.ErrUnauthorized
	}
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `UPDATE household_people SET display_name=?,relationship=?,birth_date=?,visibility=?,version=version+1,updated_at=? WHERE id=? AND household_id=? AND version=? AND archived=0`, draft.Name, draft.Relationship, draft.BirthDate, draft.Visibility, stamp, id, actor.HouseholdID, version)
	if err != nil {
		return Person{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return Person{}, policy.ErrConflict
	}
	current.Name, current.Relationship, current.BirthDate, current.Visibility = draft.Name, draft.Relationship, draft.BirthDate, draft.Visibility
	current.Version++
	current.UpdatedAt = stamp
	return current, nil
}

// Archive hides a person from Family without touching any record that names them.
func (s *Service) Archive(ctx context.Context, actor policy.ActorScope, id string, version int64) error {
	current, err := s.Get(ctx, actor, id)
	if err != nil {
		return err
	}
	if err := actor.CanMutate(policy.Resource{HouseholdID: current.HouseholdID, OwnerID: current.OwnerID, Visibility: current.Visibility, Version: current.Version}, version); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE household_people SET archived=1,version=version+1,updated_at=? WHERE id=? AND household_id=? AND version=?`, s.now().UTC().Format(time.RFC3339Nano), id, actor.HouseholdID, version)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return policy.ErrConflict
	}
	return nil
}

// Age states a whole age from a birth date: "3 weeks", "7 months" or "4 years".
// It returns "" when there is no birth date or it lies in the future.
func Age(birthDate string, now time.Time) string {
	born, err := time.Parse("2006-01-02", birthDate)
	if err != nil {
		return ""
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if born.After(today) {
		return ""
	}
	months := (today.Year()-born.Year())*12 + int(today.Month()) - int(born.Month())
	if today.Day() < born.Day() {
		months--
	}
	switch {
	case months >= 24:
		return plural(months/12, "year")
	case months >= 3:
		return plural(months, "month")
	default:
		days := int(today.Sub(born).Hours() / 24)
		if days < 14 {
			return plural(days, "day")
		}
		return plural(days/7, "week")
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

func clean(draft Draft) (Draft, error) {
	draft.Name = strings.Join(strings.Fields(draft.Name), " ")
	draft.Relationship = strings.ToLower(strings.TrimSpace(draft.Relationship))
	draft.BirthDate = strings.TrimSpace(draft.BirthDate)
	if draft.Visibility == "" {
		draft.Visibility = policy.Shared
	}
	if draft.Name == "" || utf8.RuneCountInString(draft.Name) > 80 || (draft.Visibility != policy.Shared && draft.Visibility != policy.Personal) {
		return Draft{}, ErrInvalid
	}
	known := false
	for _, r := range Relationships {
		if r == draft.Relationship {
			known = true
		}
	}
	if !known {
		return Draft{}, ErrInvalid
	}
	if draft.BirthDate != "" {
		if _, err := time.Parse("2006-01-02", draft.BirthDate); err != nil {
			return Draft{}, ErrInvalid
		}
	}
	return draft, nil
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func authorize(ctx context.Context, q queryer, a policy.ActorScope) error {
	if !a.Valid() {
		return policy.ErrUnauthorized
	}
	var one int
	if err := q.QueryRowContext(ctx, `SELECT 1 FROM household_members m JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id WHERE m.household_id=? AND m.user_id=? AND u.status='active' AND h.status='active'`, a.HouseholdID, a.ActorID).Scan(&one); err != nil {
		return policy.ErrUnauthorized
	}
	return nil
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
