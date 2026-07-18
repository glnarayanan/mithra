// Package planning stores owned household plans and calendar events.
package planning

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/glnarayanan/mithra/internal/policy"
)

var ErrInvalidRecord = errors.New("planning record is invalid")

type ScopeFilter string

const (
	AllRecords      ScopeFilter = "all"
	SharedRecords   ScopeFilter = "shared"
	PersonalRecords ScopeFilter = "personal"
)

type Provenance struct {
	SourceID, SourceFamily                                string
	SourceVersion                                         int64
	LocatorKind, LocatorValue, GeneratedBy, SchemaVersion string
}
type GoalDraft struct {
	Visibility              policy.Visibility
	Title, TargetOn, Status string
	Provenance              Provenance
}
type PlanDraft struct {
	Visibility            policy.Visibility
	GoalID, Title, Status string
	Provenance            Provenance
}
type MilestoneDraft struct {
	Visibility                   policy.Visibility
	PlanID, Title, DueOn, Status string
	Provenance                   Provenance
}
type EventDraft struct {
	Visibility                                           policy.Visibility
	PlanID, MilestoneID, Title, Description, Location    string
	AllDay                                               bool
	StartsOn, EndsOn, StartsAt, EndsAt, Timezone, Status string
	OwnerIDs                                             []string
	DependsOn                                            []string
	Constraints                                          []Constraint
	Provenance                                           Provenance
}
type Constraint struct{ Kind, Value string }
type Event struct {
	ID, HouseholdID, OwnerID                                                                  string
	Visibility                                                                                policy.Visibility
	PlanID, MilestoneID, Title, Description, Location                                         string
	AllDay                                                                                    bool
	StartsOn, EndsOn, StartsAt, EndsAt, Timezone, Status, SourceID, LocatorKind, LocatorValue string
	CreatedAt                                                                                 string
	Version                                                                                   int64
	OwnerIDs, DependsOn                                                                       []string
	Constraints                                                                               []Constraint
}
type Conflict struct {
	First, Second Event
	Reason        string
}
type Goal struct {
	ID, Title, TargetOn, Status, SourceID string
	Visibility                            policy.Visibility
}
type Plan struct {
	ID, GoalID, Title, Status, SourceID string
	Visibility                          policy.Visibility
}
type Milestone struct {
	ID, PlanID, Title, DueOn, Status, SourceID string
	Visibility                                 policy.Visibility
}
type PlanSummary struct {
	Goals      []Goal
	Plans      []Plan
	Milestones []Milestone
	Events     []Event
}
type Service struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB) *Service { return &Service{db: db, now: time.Now} }

// GetTimezone returns the household's explicitly confirmed IANA timezone.
func (s *Service) GetTimezone(ctx context.Context, a policy.ActorScope) (string, error) {
	if err := authorize(ctx, s.db, a); err != nil {
		return "", err
	}
	var zone string
	if err := s.db.QueryRowContext(ctx, `SELECT timezone FROM households WHERE id=? AND status='active'`, a.HouseholdID).Scan(&zone); err != nil {
		return "", policy.ErrUnauthorized
	}
	return zone, nil
}

// SetTimezone changes the household setting only when its active owner confirms it.
func (s *Service) SetTimezone(ctx context.Context, a policy.ActorScope, zone string) error {
	if !a.Valid() {
		return policy.ErrUnauthorized
	}
	if strings.TrimSpace(zone) != zone || len(zone) == 0 || len(zone) > 64 {
		return ErrInvalidRecord
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return ErrInvalidRecord
	}
	r, err := s.db.ExecContext(ctx, `UPDATE households SET timezone=?,updated_at=? WHERE id=? AND owner_user_id=? AND status='active' AND EXISTS (SELECT 1 FROM household_members m JOIN users u ON u.id=m.user_id WHERE m.household_id=households.id AND m.user_id=? AND m.role='owner' AND u.status='active')`, zone, s.now().UTC().Format(time.RFC3339Nano), a.HouseholdID, a.ActorID, a.ActorID)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return policy.ErrUnauthorized
	}
	return nil
}

func (s *Service) CreateGoal(ctx context.Context, a policy.ActorScope, d GoalDraft) (string, error) {
	d.Visibility = policy.PersonalDefault(d.Visibility)
	if strings.TrimSpace(d.Title) == "" || (d.TargetOn != "" && !date(d.TargetOn)) {
		return "", ErrInvalidRecord
	}
	return s.insertSimple(ctx, a, "planning_goals", d.Visibility, d.Provenance, strings.TrimSpace(d.Title), []string{"title", "target_on", "status"}, []any{strings.TrimSpace(d.Title), d.TargetOn, status(d.Status, "active", "active", "completed", "cancelled")})
}
func (s *Service) CreatePlan(ctx context.Context, a policy.ActorScope, d PlanDraft) (string, error) {
	d.Visibility = policy.PersonalDefault(d.Visibility)
	if strings.TrimSpace(d.Title) == "" {
		return "", ErrInvalidRecord
	}
	return s.insertSimple(ctx, a, "planning_plans", d.Visibility, d.Provenance, strings.TrimSpace(d.Title), []string{"goal_id", "title", "status"}, []any{nullable(d.GoalID), strings.TrimSpace(d.Title), status(d.Status, "active", "active", "completed", "cancelled")})
}
func (s *Service) CreateMilestone(ctx context.Context, a policy.ActorScope, d MilestoneDraft) (string, error) {
	d.Visibility = policy.PersonalDefault(d.Visibility)
	if d.PlanID == "" || strings.TrimSpace(d.Title) == "" || (d.DueOn != "" && !date(d.DueOn)) {
		return "", ErrInvalidRecord
	}
	return s.insertSimple(ctx, a, "planning_milestones", d.Visibility, d.Provenance, strings.TrimSpace(d.Title), []string{"plan_id", "title", "due_on", "status"}, []any{d.PlanID, strings.TrimSpace(d.Title), d.DueOn, status(d.Status, "open", "open", "completed", "cancelled")})
}

func (s *Service) CreateEvent(ctx context.Context, a policy.ActorScope, d EventDraft) (Event, error) {
	d.Visibility = policy.PersonalDefault(d.Visibility)
	if !validEvent(d) || !validProvenance(d.Provenance) || !a.Valid() {
		return Event{}, ErrInvalidRecord
	}
	id, err := newID()
	if err != nil {
		return Event{}, ErrInvalidRecord
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, err
	}
	defer tx.Rollback()
	if err = authorize(ctx, tx, a); err != nil {
		return Event{}, err
	}
	rev, err := revision(ctx, tx, a, d.Visibility)
	if err != nil {
		return Event{}, err
	}
	p := d.Provenance
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO planning_events(id,household_id,owner_user_id,visibility,source_id,source_family,source_version,plan_id,milestone_id,title,description,location,all_day,starts_on,ends_on,starts_at,ends_at,timezone,status,generated_by,schema_version,data_revision,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, a.HouseholdID, a.ActorID, d.Visibility, p.SourceID, p.SourceFamily, p.SourceVersion, nullable(d.PlanID), nullable(d.MilestoneID), strings.TrimSpace(d.Title), strings.TrimSpace(d.Description), strings.TrimSpace(d.Location), boolInt(d.AllDay), d.StartsOn, d.EndsOn, d.StartsAt, d.EndsAt, d.Timezone, status(d.Status, "planned", "planned", "completed", "cancelled"), generated(p.GeneratedBy), schema(p.SchemaVersion), rev, stamp, stamp)
	if err != nil {
		return Event{}, err
	}
	for _, owner := range unique(d.OwnerIDs) {
		if _, err = tx.ExecContext(ctx, `INSERT INTO planning_event_owners(event_id,user_id) VALUES(?,?)`, id, owner); err != nil {
			return Event{}, err
		}
	}
	for _, dep := range unique(d.DependsOn) {
		linkID, idErr := newID()
		if idErr != nil {
			return Event{}, ErrInvalidRecord
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO planning_dependencies(id,event_id,depends_on_event_id) VALUES(?,?,?)`, linkID, id, dep); err != nil {
			return Event{}, err
		}
	}
	for _, c := range d.Constraints {
		if strings.TrimSpace(c.Kind) == "" || strings.TrimSpace(c.Value) == "" {
			return Event{}, ErrInvalidRecord
		}
		constraintID, idErr := newID()
		if idErr != nil {
			return Event{}, ErrInvalidRecord
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO planning_constraints(id,event_id,kind,value) VALUES(?,?,?,?)`, constraintID, id, strings.TrimSpace(c.Kind), strings.TrimSpace(c.Value)); err != nil {
			return Event{}, err
		}
	}
	if err = link(ctx, tx, id, a, d.Visibility, p, d.Title); err != nil {
		return Event{}, err
	}
	if err = tx.Commit(); err != nil {
		return Event{}, err
	}
	return s.GetEvent(ctx, a, id)
}

func (s *Service) CompleteEvent(ctx context.Context, a policy.ActorScope, id string, expected int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = authorize(ctx, tx, a); err != nil {
		return err
	}
	e, err := find(ctx, tx, a, id)
	if err != nil {
		return err
	}
	if err = a.CanMutate(policy.Resource{HouseholdID: e.HouseholdID, OwnerID: e.OwnerID, Visibility: e.Visibility, Version: e.Version}, expected); err != nil {
		return err
	}
	r, err := tx.ExecContext(ctx, `UPDATE planning_events SET status='completed',version=version+1,updated_at=? WHERE id=? AND active=1 AND version=?`, s.now().UTC().Format(time.RFC3339Nano), id, expected)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return policy.ErrConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO planning_completion_states(event_id,completed_at,completed_by_user_id) VALUES(?,?,?)`, id, s.now().UTC().Format(time.RFC3339Nano), a.ActorID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Events is the single eligible-event query used by month, week, and agenda.
func (s *Service) Events(ctx context.Context, a policy.ActorScope, filter ScopeFilter, from, to string) ([]Event, error) {
	if from != "" && !date(from) || to != "" && !date(to) {
		return nil, ErrInvalidRecord
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = authorize(ctx, tx, a); err != nil {
		return nil, err
	}
	where, args := scope(a, filter)
	where += ` AND status='planned'`
	if from != "" {
		where += ` AND COALESCE(NULLIF(ends_on,''),NULLIF(substr(ends_at,1,10),''),starts_on,NULLIF(substr(starts_at,1,10),''))>=?`
		args = append(args, from)
	}
	if to != "" {
		where += ` AND COALESCE(NULLIF(starts_on,''),NULLIF(substr(starts_at,1,10),''))<=?`
		args = append(args, to)
	}
	query := `SELECT id,household_id,owner_user_id,visibility,COALESCE(plan_id,''),COALESCE(milestone_id,''),title,description,location,all_day,starts_on,ends_on,starts_at,ends_at,timezone,status,source_id,COALESCE((SELECT locator_kind FROM evidence_links l WHERE l.record_family='planning' AND l.record_id=planning_events.id AND l.household_id=planning_events.household_id AND l.source_id=planning_events.source_id LIMIT 1),''),COALESCE((SELECT locator_value FROM evidence_links l WHERE l.record_family='planning' AND l.record_id=planning_events.id AND l.household_id=planning_events.household_id AND l.source_id=planning_events.source_id LIMIT 1),''),version,created_at FROM planning_events WHERE ` + where + ` ORDER BY COALESCE(NULLIF(starts_on,''),NULLIF(substr(starts_at,1,10),'')),starts_at,id`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var v string
		var all int
		if err = rows.Scan(&e.ID, &e.HouseholdID, &e.OwnerID, &v, &e.PlanID, &e.MilestoneID, &e.Title, &e.Description, &e.Location, &all, &e.StartsOn, &e.EndsOn, &e.StartsAt, &e.EndsAt, &e.Timezone, &e.Status, &e.SourceID, &e.LocatorKind, &e.LocatorValue, &e.Version, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Visibility = policy.Visibility(v)
		e.AllDay = all == 1
		e.OwnerIDs, e.DependsOn, e.Constraints = owners(ctx, tx, e.ID), deps(ctx, tx, e.ID), constraints(ctx, tx, e.ID)
		out = append(out, e)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
func (s *Service) Conflicts(ctx context.Context, a policy.ActorScope, filter ScopeFilter, from, to string) ([]Conflict, error) {
	events, err := s.Events(ctx, a, filter, from, to)
	if err != nil {
		return nil, err
	}
	var out []Conflict
	for i := range events {
		for j := i + 1; j < len(events); j++ {
			if overlaps(events[i], events[j]) && shares(events[i].OwnerIDs, events[j].OwnerIDs) {
				out = append(out, Conflict{events[i], events[j], "Assigned owners have overlapping events."})
			}
		}
	}
	return out, nil
}

// GetEvent returns one authorized event, including a completed event for export.
func (s *Service) GetEvent(ctx context.Context, a policy.ActorScope, id string) (Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, err
	}
	defer tx.Rollback()
	if err = authorize(ctx, tx, a); err != nil {
		return Event{}, err
	}
	e, err := find(ctx, tx, a, id)
	if err != nil {
		return Event{}, err
	}
	e.OwnerIDs, e.DependsOn, e.Constraints = owners(ctx, tx, e.ID), deps(ctx, tx, e.ID), constraints(ctx, tx, e.ID)
	if err = tx.Commit(); err != nil {
		return Event{}, err
	}
	return e, nil
}

// Plans provides the authorized planning hierarchy for the lens.
func (s *Service) Plans(ctx context.Context, a policy.ActorScope, filter ScopeFilter) (PlanSummary, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PlanSummary{}, err
	}
	defer tx.Rollback()
	if err = authorize(ctx, tx, a); err != nil {
		return PlanSummary{}, err
	}
	where, args := scope(a, filter)
	out := PlanSummary{}
	rows, err := tx.QueryContext(ctx, `SELECT id,title,target_on,status,visibility,source_id FROM planning_goals WHERE `+where+` ORDER BY target_on,id`, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var x Goal
		var v string
		if err = rows.Scan(&x.ID, &x.Title, &x.TargetOn, &x.Status, &v, &x.SourceID); err != nil {
			rows.Close()
			return out, err
		}
		x.Visibility = policy.Visibility(v)
		out.Goals = append(out.Goals, x)
	}
	if err = rows.Close(); err != nil {
		return out, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT id,COALESCE(goal_id,''),title,status,visibility,source_id FROM planning_plans WHERE `+where+` ORDER BY title,id`, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var x Plan
		var v string
		if err = rows.Scan(&x.ID, &x.GoalID, &x.Title, &x.Status, &v, &x.SourceID); err != nil {
			rows.Close()
			return out, err
		}
		x.Visibility = policy.Visibility(v)
		out.Plans = append(out.Plans, x)
	}
	if err = rows.Close(); err != nil {
		return out, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT id,plan_id,title,due_on,status,visibility,source_id FROM planning_milestones WHERE `+where+` ORDER BY due_on,id`, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var x Milestone
		var v string
		if err = rows.Scan(&x.ID, &x.PlanID, &x.Title, &x.DueOn, &x.Status, &v, &x.SourceID); err != nil {
			rows.Close()
			return out, err
		}
		x.Visibility = policy.Visibility(v)
		out.Milestones = append(out.Milestones, x)
	}
	if err = rows.Close(); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	out.Events, err = s.Events(ctx, a, filter, "", "9999-12-31")
	return out, err
}

func (s *Service) insertSimple(ctx context.Context, a policy.ActorScope, table string, v policy.Visibility, p Provenance, content string, cols []string, values []any) (string, error) {
	if !a.Valid() || !validProvenance(p) {
		return "", ErrInvalidRecord
	}
	id, err := newID()
	if err != nil {
		return "", ErrInvalidRecord
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err = authorize(ctx, tx, a); err != nil {
		return "", err
	}
	rev, err := revision(ctx, tx, a, v)
	if err != nil {
		return "", err
	}
	names := append([]string{"id", "household_id", "owner_user_id", "visibility", "source_id", "source_family", "source_version"}, cols...)
	names = append(names, "generated_by", "schema_version", "data_revision", "created_at", "updated_at")
	args := []any{id, a.HouseholdID, a.ActorID, v, p.SourceID, p.SourceFamily, p.SourceVersion}
	args = append(args, values...)
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	args = append(args, generated(p.GeneratedBy), schema(p.SchemaVersion), rev, stamp, stamp)
	q := "INSERT INTO " + table + "(" + strings.Join(names, ",") + ") VALUES(" + strings.TrimRight(strings.Repeat("?,", len(args)), ",") + ")"
	if _, err = tx.ExecContext(ctx, q, args...); err != nil {
		return "", err
	}
	if err = link(ctx, tx, id, a, v, p, content); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

type rower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func authorize(ctx context.Context, q rower, a policy.ActorScope) error {
	if !a.Valid() {
		return policy.ErrUnauthorized
	}
	var one int
	if err := q.QueryRowContext(ctx, `SELECT 1 FROM household_members m JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id WHERE m.household_id=? AND m.user_id=? AND u.status='active' AND h.status='active'`, a.HouseholdID, a.ActorID).Scan(&one); err != nil {
		return policy.ErrUnauthorized
	}
	return nil
}
func revision(ctx context.Context, q rower, a policy.ActorScope, v policy.Visibility) (int64, error) {
	var r int64
	qry := `SELECT personal_revision FROM user_revisions WHERE user_id=?`
	arg := a.ActorID
	if v == policy.Shared {
		qry = `SELECT shared_revision FROM household_revisions WHERE household_id=?`
		arg = a.HouseholdID
	}
	return r, q.QueryRowContext(ctx, qry, arg).Scan(&r)
}
func find(ctx context.Context, q interface {
	rower
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, a policy.ActorScope, id string) (Event, error) {
	rows, err := eventRows(ctx, q, `id=? AND active=1 AND household_id=? AND (visibility='shared' OR owner_user_id=?)`, id, a.HouseholdID, a.ActorID)
	if err != nil || len(rows) == 0 {
		if err == nil {
			err = policy.ErrUnauthorized
		}
		return Event{}, err
	}
	return rows[0], nil
}
func eventRows(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, where string, args ...any) ([]Event, error) {
	rows, err := q.QueryContext(ctx, `SELECT id,household_id,owner_user_id,visibility,COALESCE(plan_id,''),COALESCE(milestone_id,''),title,description,location,all_day,starts_on,ends_on,starts_at,ends_at,timezone,status,source_id,COALESCE((SELECT locator_kind FROM evidence_links l WHERE l.record_family='planning' AND l.record_id=planning_events.id AND l.household_id=planning_events.household_id AND l.source_id=planning_events.source_id LIMIT 1),''),COALESCE((SELECT locator_value FROM evidence_links l WHERE l.record_family='planning' AND l.record_id=planning_events.id AND l.household_id=planning_events.household_id AND l.source_id=planning_events.source_id LIMIT 1),''),version,created_at FROM planning_events WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var v string
		var all int
		if err := rows.Scan(&e.ID, &e.HouseholdID, &e.OwnerID, &v, &e.PlanID, &e.MilestoneID, &e.Title, &e.Description, &e.Location, &all, &e.StartsOn, &e.EndsOn, &e.StartsAt, &e.EndsAt, &e.Timezone, &e.Status, &e.SourceID, &e.LocatorKind, &e.LocatorValue, &e.Version, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Visibility = policy.Visibility(v)
		e.AllDay = all == 1
		out = append(out, e)
	}
	return out, rows.Err()
}
func scope(a policy.ActorScope, f ScopeFilter) (string, []any) {
	w := `household_id=? AND active=1 AND (visibility='shared' OR owner_user_id=?)`
	args := []any{a.HouseholdID, a.ActorID}
	if f == SharedRecords {
		w += ` AND visibility='shared'`
	} else if f == PersonalRecords {
		w += ` AND visibility='personal' AND owner_user_id=?`
		args = append(args, a.ActorID)
	}
	return w, args
}
func owners(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, id string) []string {
	return stringsFor(ctx, q, `SELECT o.user_id FROM planning_event_owners o JOIN planning_events e ON e.id=o.event_id JOIN household_members m ON m.household_id=e.household_id AND m.user_id=o.user_id JOIN users u ON u.id=o.user_id WHERE o.event_id=? AND u.status='active' ORDER BY o.user_id`, id)
}
func deps(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, id string) []string {
	return stringsFor(ctx, q, `SELECT depends_on_event_id FROM planning_dependencies WHERE event_id=? ORDER BY depends_on_event_id`, id)
}
func constraints(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, id string) []Constraint {
	rows, err := q.QueryContext(ctx, `SELECT kind,value FROM planning_constraints WHERE event_id=? ORDER BY kind,value`, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var x []Constraint
	for rows.Next() {
		var c Constraint
		if rows.Scan(&c.Kind, &c.Value) == nil {
			x = append(x, c)
		}
	}
	return x
}
func stringsFor(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, sql string, id string) []string {
	rows, err := q.QueryContext(ctx, sql, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var x []string
	for rows.Next() {
		var v string
		if rows.Scan(&v) == nil {
			x = append(x, v)
		}
	}
	return x
}
func link(ctx context.Context, tx *sql.Tx, id string, a policy.ActorScope, v policy.Visibility, p Provenance, content string) error {
	e, err := newID()
	if err != nil {
		return ErrInvalidRecord
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO evidence_links(id,record_family,record_id,household_id,owner_user_id,visibility,source_id,source_family,source_version,locator_kind,locator_value,created_at) VALUES(?,'planning',?,?,?,?,?,?,?,?,?,?)`, e, id, a.HouseholdID, a.ActorID, v, p.SourceID, p.SourceFamily, p.SourceVersion, p.LocatorKind, p.LocatorValue, stamp); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO search_entries(record_family,record_id,household_id,owner_user_id,visibility,content) VALUES('planning',?,?,?,?,?)`, id, a.HouseholdID, a.ActorID, v, strings.TrimSpace(content))
	return err
}
func validProvenance(p Provenance) bool {
	if p.SourceID == "" || p.SourceVersion < 1 || p.LocatorKind == "" || p.LocatorValue == "" {
		return false
	}
	switch p.SourceFamily {
	case "text", "voice", "csv", "xlsx", "pdf":
		return true
	}
	return false
}
func validEvent(d EventDraft) bool {
	if strings.TrimSpace(d.Title) == "" {
		return false
	}
	if d.AllDay {
		return date(d.StartsOn) && (d.EndsOn == "" || (date(d.EndsOn) && d.EndsOn >= d.StartsOn)) && d.StartsAt == "" && d.EndsAt == ""
	}
	if d.StartsAt == "" || d.EndsAt == "" || d.Timezone == "" {
		return false
	}
	loc, e := time.LoadLocation(d.Timezone)
	if e != nil {
		return false
	}
	a, e := time.ParseInLocation("2006-01-02T15:04", d.StartsAt, loc)
	if e != nil {
		return false
	}
	b, e := time.ParseInLocation("2006-01-02T15:04", d.EndsAt, loc)
	return e == nil && b.After(a)
}
func date(v string) bool { _, e := time.Parse("2006-01-02", v); return e == nil }
func status(v, def string, allowed ...string) string {
	if v == "" {
		return def
	}
	for _, x := range allowed {
		if v == x {
			return v
		}
	}
	return ""
}
func generated(v string) string {
	if v == "" {
		return "application"
	}
	return v
}
func schema(v string) string {
	if v == "" {
		return "planning-v1"
	}
	return v
}
func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func newID() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	return hex.EncodeToString(b[:]), nil
}
func unique(in []string) []string {
	m := map[string]bool{}
	for _, v := range in {
		if v != "" {
			m[v] = true
		}
	}
	out := make([]string, 0, len(m))
	for v := range m {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
func overlaps(a, b Event) bool {
	as, ae := bounds(a)
	bs, be := bounds(b)
	return as.Before(be) && bs.Before(ae)
}
func bounds(e Event) (time.Time, time.Time) {
	if e.AllDay {
		s, _ := time.Parse("2006-01-02", e.StartsOn)
		end := e.EndsOn
		if end == "" {
			end = e.StartsOn
		}
		z, _ := time.Parse("2006-01-02", end)
		return s, z.AddDate(0, 0, 1)
	}
	loc, _ := time.LoadLocation(e.Timezone)
	s, _ := time.ParseInLocation("2006-01-02T15:04", e.StartsAt, loc)
	z, _ := time.ParseInLocation("2006-01-02T15:04", e.EndsAt, loc)
	return s, z
}
func shares(a, b []string) bool {
	m := map[string]bool{}
	for _, x := range a {
		m[x] = true
	}
	for _, x := range b {
		if m[x] {
			return true
		}
	}
	return false
}
