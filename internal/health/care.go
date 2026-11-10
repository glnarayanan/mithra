package health

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/glnarayanan/mithra/internal/policy"
)

// Medicines, notes from appointments and habits are typed directly by an
// adult. They are facts as entered: Mithra never suggests a dose, a medicine
// or a habit.

type Medicine struct {
	ID, OwnerID, Subject, Name, Dosage, StartedOn, EndedOn, Note string
	Visibility                                                   policy.Visibility
	Version                                                      int64
}

type MedicineDraft struct {
	Subject, Name, Dosage, StartedOn, Note string
	Visibility                             policy.Visibility
}

type Note struct {
	ID, OwnerID, Subject, RecordedOn, Content string
	Visibility                                policy.Visibility
}

type NoteDraft struct {
	Subject, RecordedOn, Content string
	Visibility                   policy.Visibility
}

type Habit struct {
	ID, OwnerID, Subject, Name, Kind, Unit string
	Visibility                             policy.Visibility
	// Logs maps an ISO date to the value logged that day.
	Logs map[string]string
}

type HabitDraft struct {
	Subject, Name, Kind, Unit string
	Visibility                policy.Visibility
}

// CareLog is everything typed directly for the people the actor can see.
type CareLog struct {
	Medicines []Medicine
	Notes     []Note
	Habits    []Habit
}

func fits(value string, min, max int) bool {
	n := utf8.RuneCountInString(value)
	return n >= min && n <= max
}

func visibilityOrDefault(v policy.Visibility) policy.Visibility {
	if v == policy.Shared {
		return policy.Shared
	}
	return policy.Personal
}

func (s *Service) CreateMedicine(ctx context.Context, actor policy.ActorScope, draft MedicineDraft) (Medicine, error) {
	m := Medicine{Subject: strings.TrimSpace(draft.Subject), Name: strings.TrimSpace(draft.Name), Dosage: strings.TrimSpace(draft.Dosage), StartedOn: strings.TrimSpace(draft.StartedOn), Note: strings.TrimSpace(draft.Note), Visibility: visibilityOrDefault(draft.Visibility), OwnerID: actor.ActorID, Version: 1}
	if !fits(m.Subject, 1, 128) || !fits(m.Name, 1, 128) || !fits(m.Dosage, 0, 128) || !fits(m.Note, 0, 512) || (m.StartedOn != "" && !validDate(m.StartedOn)) {
		return Medicine{}, ErrInvalidRecord
	}
	if err := authorize(ctx, s.db, actor); err != nil {
		return Medicine{}, err
	}
	id, err := healthID()
	if err != nil {
		return Medicine{}, err
	}
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO health_medicines(id,household_id,owner_user_id,visibility,subject,name,dosage,started_on,note,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,1,?,?)`, id, actor.HouseholdID, actor.ActorID, m.Visibility, m.Subject, m.Name, m.Dosage, m.StartedOn, m.Note, stamp, stamp); err != nil {
		return Medicine{}, err
	}
	m.ID = id
	return m, nil
}

// EndMedicine records the day a medicine stopped.
func (s *Service) EndMedicine(ctx context.Context, actor policy.ActorScope, id string, version int64, endedOn string) error {
	if !validDate(endedOn) {
		return ErrInvalidRecord
	}
	if err := authorize(ctx, s.db, actor); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE health_medicines SET ended_on=?,version=version+1,updated_at=? WHERE id=? AND household_id=? AND version=? AND ended_on='' AND (visibility='shared' OR owner_user_id=?)`, endedOn, s.now().UTC().Format(time.RFC3339Nano), id, actor.HouseholdID, version, actor.ActorID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return policy.ErrConflict
	}
	return nil
}

func (s *Service) CreateNote(ctx context.Context, actor policy.ActorScope, draft NoteDraft) (Note, error) {
	n := Note{Subject: strings.TrimSpace(draft.Subject), RecordedOn: strings.TrimSpace(draft.RecordedOn), Content: strings.TrimSpace(draft.Content), Visibility: visibilityOrDefault(draft.Visibility), OwnerID: actor.ActorID}
	if !fits(n.Subject, 1, 128) || !validDate(n.RecordedOn) || !fits(n.Content, 1, 4000) {
		return Note{}, ErrInvalidRecord
	}
	if err := authorize(ctx, s.db, actor); err != nil {
		return Note{}, err
	}
	id, err := healthID()
	if err != nil {
		return Note{}, err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO health_notes(id,household_id,owner_user_id,visibility,subject,recorded_on,content,created_at) VALUES(?,?,?,?,?,?,?,?)`, id, actor.HouseholdID, actor.ActorID, n.Visibility, n.Subject, n.RecordedOn, n.Content, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
		return Note{}, err
	}
	n.ID = id
	return n, nil
}

func (s *Service) CreateHabit(ctx context.Context, actor policy.ActorScope, draft HabitDraft) (Habit, error) {
	h := Habit{Subject: strings.TrimSpace(draft.Subject), Name: strings.TrimSpace(draft.Name), Kind: strings.TrimSpace(draft.Kind), Unit: strings.TrimSpace(draft.Unit), Visibility: visibilityOrDefault(draft.Visibility), OwnerID: actor.ActorID, Logs: map[string]string{}}
	if h.Kind == "" {
		h.Kind = "check"
	}
	if !fits(h.Subject, 1, 128) || !fits(h.Name, 1, 128) || (h.Kind != "check" && h.Kind != "number") || !fits(h.Unit, 0, 32) {
		return Habit{}, ErrInvalidRecord
	}
	if err := authorize(ctx, s.db, actor); err != nil {
		return Habit{}, err
	}
	id, err := healthID()
	if err != nil {
		return Habit{}, err
	}
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO health_habits(id,household_id,owner_user_id,visibility,subject,name,kind,unit,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, actor.HouseholdID, actor.ActorID, h.Visibility, h.Subject, h.Name, h.Kind, h.Unit, stamp, stamp); err != nil {
		return Habit{}, err
	}
	h.ID = id
	return h, nil
}

// LogHabit records one day's value, replacing an earlier value for that day.
// An empty value clears the day.
func (s *Service) LogHabit(ctx context.Context, actor policy.ActorScope, habitID, day, value string) error {
	value = strings.TrimSpace(value)
	if !validDate(day) || !fits(value, 0, 64) {
		return ErrInvalidRecord
	}
	if err := authorize(ctx, s.db, actor); err != nil {
		return err
	}
	var kind string
	if err := s.db.QueryRowContext(ctx, `SELECT kind FROM health_habits WHERE id=? AND household_id=? AND active=1 AND (visibility='shared' OR owner_user_id=?)`, habitID, actor.HouseholdID, actor.ActorID).Scan(&kind); err != nil {
		return policy.ErrUnauthorized
	}
	if value == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM health_habit_logs WHERE habit_id=? AND logged_on=?`, habitID, day)
		return err
	}
	if kind == "number" {
		if _, err := ParseValue(value); err != nil {
			return ErrInvalidRecord
		}
	} else if value != "yes" {
		return ErrInvalidRecord
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO health_habit_logs(habit_id,logged_on,value_text,created_at) VALUES(?,?,?,?) ON CONFLICT(habit_id,logged_on) DO UPDATE SET value_text=excluded.value_text`, habitID, day, value, s.now().UTC().Format(time.RFC3339Nano))
	return err
}

// StopHabit hides a habit; its past logs stay.
func (s *Service) StopHabit(ctx context.Context, actor policy.ActorScope, habitID string) error {
	if err := authorize(ctx, s.db, actor); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE health_habits SET active=0,updated_at=? WHERE id=? AND household_id=? AND (visibility='shared' OR owner_user_id=?)`, s.now().UTC().Format(time.RFC3339Nano), habitID, actor.HouseholdID, actor.ActorID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return policy.ErrUnauthorized
	}
	return nil
}

// CareLogFor lists the typed care entries for one subject ("" lists all),
// with habit logs since the given day.
func (s *Service) CareLogFor(ctx context.Context, actor policy.ActorScope, subject, logsSince string) (CareLog, error) {
	var log CareLog
	if err := authorize(ctx, s.db, actor); err != nil {
		return log, err
	}
	subject = strings.TrimSpace(subject)
	visible := `household_id=? AND (visibility='shared' OR owner_user_id=?) AND (?='' OR subject=?)`
	args := []any{actor.HouseholdID, actor.ActorID, subject, subject}
	rows, err := s.db.QueryContext(ctx, `SELECT id,owner_user_id,visibility,subject,name,dosage,started_on,ended_on,note,version FROM health_medicines WHERE `+visible+` ORDER BY ended_on<>'', started_on DESC, name`, args...)
	if err != nil {
		return log, err
	}
	for rows.Next() {
		var m Medicine
		if err := rows.Scan(&m.ID, &m.OwnerID, &m.Visibility, &m.Subject, &m.Name, &m.Dosage, &m.StartedOn, &m.EndedOn, &m.Note, &m.Version); err != nil {
			rows.Close()
			return log, err
		}
		log.Medicines = append(log.Medicines, m)
	}
	rows.Close()
	rows, err = s.db.QueryContext(ctx, `SELECT id,owner_user_id,visibility,subject,recorded_on,content FROM health_notes WHERE `+visible+` ORDER BY recorded_on DESC, created_at DESC`, args...)
	if err != nil {
		return log, err
	}
	for rows.Next() {
		var n Note
		if err := rows.Scan(&n.ID, &n.OwnerID, &n.Visibility, &n.Subject, &n.RecordedOn, &n.Content); err != nil {
			rows.Close()
			return log, err
		}
		log.Notes = append(log.Notes, n)
	}
	rows.Close()
	rows, err = s.db.QueryContext(ctx, `SELECT id,owner_user_id,visibility,subject,name,kind,unit FROM health_habits WHERE active=1 AND `+visible+` ORDER BY name`, args...)
	if err != nil {
		return log, err
	}
	index := map[string]int{}
	for rows.Next() {
		h := Habit{Logs: map[string]string{}}
		if err := rows.Scan(&h.ID, &h.OwnerID, &h.Visibility, &h.Subject, &h.Name, &h.Kind, &h.Unit); err != nil {
			rows.Close()
			return log, err
		}
		index[h.ID] = len(log.Habits)
		log.Habits = append(log.Habits, h)
	}
	rows.Close()
	if len(log.Habits) > 0 {
		rows, err = s.db.QueryContext(ctx, `SELECT l.habit_id,l.logged_on,l.value_text FROM health_habit_logs l JOIN health_habits h ON h.id=l.habit_id WHERE h.household_id=? AND l.logged_on>=?`, actor.HouseholdID, logsSince)
		if err != nil {
			return log, err
		}
		for rows.Next() {
			var id, day, value string
			if err := rows.Scan(&id, &day, &value); err != nil {
				rows.Close()
				return log, err
			}
			if i, ok := index[id]; ok {
				log.Habits[i].Logs[day] = value
			}
		}
		rows.Close()
	}
	return log, nil
}
