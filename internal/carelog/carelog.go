// Package carelog is the baby log: feeds, diapers, spit-ups, weights and
// milestones for a person the household looks after. Entries are shared by
// both adults, so either can log and both see the same record. It states what
// was logged and never interprets it.
package carelog

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/glnarayanan/mithra/internal/policy"
)

var (
	ErrInvalid  = errors.New("care entry is invalid")
	ErrNotFound = errors.New("care entry not found")
)

// InputError carries a message a person can act on.
type InputError struct{ Message string }

func (e *InputError) Error() string { return e.Message }

const (
	Feed      = "feed"
	Diaper    = "diaper"
	SpitUp    = "spitup"
	Weight    = "weight"
	Milestone = "milestone"
)

type Event struct {
	ID, PersonID, ActorID, ActorEmail, Type, Source, ClientID string
	OccurredAt, EndedAt                                       time.Time
	Details                                                   map[string]string
	// Duplicate is set by Create when the client id was already logged.
	Duplicate bool
}

// Running reports a breast feed that was started and not yet finished.
func (e Event) Running() bool {
	return e.Type == Feed && e.EndedAt.IsZero() && e.Details["running"] == "yes"
}

type Input struct {
	Type       string
	OccurredAt time.Time
	EndedAt    time.Time
	ClientID   string
	Source     string
	Fields     map[string]string
}

type Service struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB) *Service { return &Service{db: db, now: time.Now} }

func (s *Service) WithClock(now func() time.Time) *Service { return &Service{db: s.db, now: now} }

func choose(value string, allowed ...string) bool {
	for _, a := range allowed {
		if value == a {
			return true
		}
	}
	return false
}

func number(fields map[string]string, key string, min, max float64, label string) (string, error) {
	raw := strings.TrimSpace(fields[key])
	if raw == "" {
		return "", nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value < min || value > max {
		return "", &InputError{label + " must be a number from " + strconv.FormatFloat(min, 'f', -1, 64) + " to " + strconv.FormatFloat(max, 'f', -1, 64) + "."}
	}
	return strconv.FormatFloat(value, 'f', -1, 64), nil
}

// Validate shapes the form fields for one entry type.
func Validate(in Input) (map[string]string, error) {
	f := in.Fields
	if f == nil {
		f = map[string]string{}
	}
	note := strings.TrimSpace(f["note"])
	if utf8.RuneCountInString(note) > 500 {
		return nil, &InputError{"Keep the note to 500 characters or fewer."}
	}
	out := map[string]string{}
	if note != "" {
		out["note"] = note
	}
	switch in.Type {
	case Feed:
		mode := strings.TrimSpace(f["mode"])
		if !choose(mode, "breast", "bottle", "combo") {
			return nil, &InputError{"Choose breast, bottle or both."}
		}
		out["mode"] = mode
		if mode != "bottle" {
			side := strings.TrimSpace(f["side"])
			if !choose(side, "left", "right", "both") {
				return nil, &InputError{"Choose left, right or both sides."}
			}
			out["side"] = side
		}
		duration, err := number(f, "duration_min", 0, 300, "Minutes")
		if err != nil {
			return nil, err
		}
		if duration != "" && duration != "0" {
			out["duration_min"] = duration
		}
		if mode != "breast" {
			milk := strings.TrimSpace(f["milk_type"])
			if milk == "" {
				milk = "formula"
			}
			if !choose(milk, "formula", "expressed") {
				return nil, &InputError{"Choose formula or expressed milk."}
			}
			out["milk_type"] = milk
			offered, err := number(f, "offered_ml", 0, 2000, "Offered millilitres")
			if err != nil {
				return nil, err
			}
			consumed, err := number(f, "consumed_ml", 0, 2000, "Consumed millilitres")
			if err != nil {
				return nil, err
			}
			if offered != "" && consumed != "" {
				o, _ := strconv.ParseFloat(offered, 64)
				c, _ := strconv.ParseFloat(consumed, 64)
				if c > o {
					return nil, &InputError{"Taken can’t be more than offered. Lower one of them."}
				}
			}
			if offered == "" && consumed == "" {
				return nil, &InputError{"Enter how much was offered or taken, in ml."}
			}
			if offered != "" {
				out["offered_ml"] = offered
			}
			if consumed != "" {
				out["consumed_ml"] = consumed
			}
		}
		if mode != "bottle" && in.EndedAt.IsZero() && out["duration_min"] == "" {
			out["running"] = "yes"
		}
	case Diaper:
		kind := strings.TrimSpace(f["kind"])
		if !choose(kind, "wet", "dirty", "both") {
			return nil, &InputError{"Choose wet, dirty or both."}
		}
		out["kind"] = kind
	case SpitUp:
		size := strings.TrimSpace(f["size"])
		if !choose(size, "small", "medium", "large") {
			return nil, &InputError{"Choose small, medium or large."}
		}
		out["size"] = size
	case Weight:
		kg, err := number(f, "kg", 0.01, 100, "Weight in kg")
		if err != nil {
			return nil, err
		}
		if kg == "" {
			return nil, &InputError{"Enter the weight in kg."}
		}
		out["kg"] = kg
	case Milestone:
		title := strings.TrimSpace(f["title"])
		if title == "" || utf8.RuneCountInString(title) > 160 {
			return nil, &InputError{"Give the milestone a title of up to 160 characters."}
		}
		out["title"] = title
	default:
		return nil, &InputError{"Choose what to log."}
	}
	return out, nil
}

// Create logs one entry. A repeated client id returns the entry already saved,
// so an offline retry never logs twice.
func (s *Service) Create(ctx context.Context, actor policy.ActorScope, personID string, in Input) (Event, error) {
	details, err := Validate(in)
	if err != nil {
		return Event{}, err
	}
	if in.OccurredAt.IsZero() {
		in.OccurredAt = s.now()
	}
	now := s.now().UTC()
	if in.OccurredAt.After(now.Add(5 * time.Minute)) {
		return Event{}, &InputError{"That time is in the future. Choose a time that has passed."}
	}
	if !in.EndedAt.IsZero() && in.EndedAt.Before(in.OccurredAt) {
		return Event{}, &InputError{"The end is before the start. Check both times."}
	}
	if in.Source == "" {
		in.Source = "web"
	}
	if err := s.visiblePerson(ctx, actor, personID); err != nil {
		return Event{}, err
	}
	if details["running"] == "yes" {
		var running int
		_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM care_events WHERE household_id=? AND person_id=? AND type='feed' AND ended_at='' AND deleted_at='' AND json_extract(details_json,'$.running')='yes' AND client_id<>?`, actor.HouseholdID, personID, in.ClientID).Scan(&running)
		if running > 0 {
			return Event{}, &InputError{"A feed is already running. Finish it before starting another."}
		}
	}
	id, err := newID()
	if err != nil {
		return Event{}, err
	}
	if strings.TrimSpace(in.ClientID) == "" || len(in.ClientID) > 128 {
		in.ClientID = "server:" + id
	}
	raw, _ := json.Marshal(details)
	ended := ""
	if !in.EndedAt.IsZero() {
		ended = in.EndedAt.UTC().Format(time.RFC3339)
	}
	stamp := now.Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `INSERT INTO care_events(id,household_id,person_id,actor_user_id,client_id,source,type,occurred_at,ended_at,details_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(household_id,client_id) DO NOTHING`, id, actor.HouseholdID, personID, actor.ActorID, in.ClientID, in.Source, in.Type, in.OccurredAt.UTC().Format(time.RFC3339), ended, string(raw), stamp, stamp)
	if err != nil {
		return Event{}, err
	}
	event, err := s.byClient(ctx, actor, in.ClientID)
	if changed, _ := result.RowsAffected(); changed == 0 {
		event.Duplicate = true
	}
	return event, err
}

// Finish ends a running feed now (or at endedAt) and records its minutes.
func (s *Service) Finish(ctx context.Context, actor policy.ActorScope, personID, eventID string, endedAt time.Time) (Event, error) {
	event, err := s.Get(ctx, actor, personID, eventID)
	if err != nil {
		return Event{}, err
	}
	if !event.Running() {
		return Event{}, &InputError{"This feed has already finished."}
	}
	if endedAt.IsZero() {
		endedAt = s.now()
	}
	if endedAt.Before(event.OccurredAt) {
		endedAt = event.OccurredAt
	}
	details := event.Details
	delete(details, "running")
	minutes := int(endedAt.Sub(event.OccurredAt).Round(time.Minute) / time.Minute)
	details["duration_min"] = strconv.Itoa(minutes)
	raw, _ := json.Marshal(details)
	if _, err := s.db.ExecContext(ctx, `UPDATE care_events SET ended_at=?,details_json=?,updated_at=? WHERE id=? AND household_id=? AND deleted_at=''`, endedAt.UTC().Format(time.RFC3339), string(raw), s.now().UTC().Format(time.RFC3339Nano), eventID, actor.HouseholdID); err != nil {
		return Event{}, err
	}
	return s.Get(ctx, actor, personID, eventID)
}

// Update corrects an entry's time and details. A running feed stays running
// unless minutes are given.
func (s *Service) Update(ctx context.Context, actor policy.ActorScope, personID, eventID string, occurredAt time.Time, fields map[string]string) (Event, error) {
	event, err := s.Get(ctx, actor, personID, eventID)
	if err != nil {
		return Event{}, err
	}
	if fields == nil {
		fields = map[string]string{}
	}
	if fields["note"] == "" {
		fields["note"] = event.Details["note"]
	}
	details, err := Validate(Input{Type: event.Type, EndedAt: event.EndedAt, Fields: fields})
	if err != nil {
		return Event{}, err
	}
	if !event.Running() {
		delete(details, "running")
	}
	if occurredAt.IsZero() {
		occurredAt = event.OccurredAt
	}
	if occurredAt.After(s.now().Add(5 * time.Minute)) {
		return Event{}, &InputError{"That time is in the future. Choose a time that has passed."}
	}
	ended := ""
	if !event.EndedAt.IsZero() {
		if event.Type == Feed && details["duration_min"] != "" {
			minutes, _ := strconv.Atoi(details["duration_min"])
			event.EndedAt = occurredAt.Add(time.Duration(minutes) * time.Minute)
		}
		ended = event.EndedAt.UTC().Format(time.RFC3339)
	} else if event.Running() && details["duration_min"] != "" {
		delete(details, "running")
		minutes, _ := strconv.Atoi(details["duration_min"])
		ended = occurredAt.Add(time.Duration(minutes) * time.Minute).UTC().Format(time.RFC3339)
	}
	raw, _ := json.Marshal(details)
	if _, err := s.db.ExecContext(ctx, `UPDATE care_events SET occurred_at=?,ended_at=?,details_json=?,updated_at=? WHERE id=? AND household_id=? AND person_id=? AND deleted_at=''`, occurredAt.UTC().Format(time.RFC3339), ended, string(raw), s.now().UTC().Format(time.RFC3339Nano), eventID, actor.HouseholdID, personID); err != nil {
		return Event{}, err
	}
	return s.Get(ctx, actor, personID, eventID)
}

// Delete hides an entry; Restore brings it back (the undo).
func (s *Service) Delete(ctx context.Context, actor policy.ActorScope, personID, eventID string) error {
	return s.setDeleted(ctx, actor, personID, eventID, s.now().UTC().Format(time.RFC3339Nano))
}

func (s *Service) Restore(ctx context.Context, actor policy.ActorScope, personID, eventID string) error {
	return s.setDeleted(ctx, actor, personID, eventID, "")
}

func (s *Service) setDeleted(ctx context.Context, actor policy.ActorScope, personID, eventID, value string) error {
	if err := s.visiblePerson(ctx, actor, personID); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE care_events SET deleted_at=?,updated_at=? WHERE id=? AND household_id=? AND person_id=?`, value, s.now().UTC().Format(time.RFC3339Nano), eventID, actor.HouseholdID, personID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) Get(ctx context.Context, actor policy.ActorScope, personID, eventID string) (Event, error) {
	events, err := s.query(ctx, actor, personID, `AND e.id=? AND e.deleted_at=''`, eventID)
	if err != nil {
		return Event{}, err
	}
	if len(events) != 1 {
		return Event{}, ErrNotFound
	}
	return events[0], nil
}

// List returns live entries since a moment, newest first.
func (s *Service) List(ctx context.Context, actor policy.ActorScope, personID string, since time.Time) ([]Event, error) {
	return s.query(ctx, actor, personID, `AND e.deleted_at='' AND e.occurred_at>=? ORDER BY e.occurred_at DESC`, since.UTC().Format(time.RFC3339))
}

// Weights returns every live weight entry, oldest first.
func (s *Service) Weights(ctx context.Context, actor policy.ActorScope, personID string) ([]Event, error) {
	return s.query(ctx, actor, personID, `AND e.deleted_at='' AND e.type='weight' ORDER BY e.occurred_at ASC`)
}

// Milestones returns every live milestone, newest first.
func (s *Service) Milestones(ctx context.Context, actor policy.ActorScope, personID string) ([]Event, error) {
	return s.query(ctx, actor, personID, `AND e.deleted_at='' AND e.type='milestone' ORDER BY e.occurred_at DESC`)
}

func (s *Service) byClient(ctx context.Context, actor policy.ActorScope, clientID string) (Event, error) {
	var personID string
	if err := s.db.QueryRowContext(ctx, `SELECT person_id FROM care_events WHERE household_id=? AND client_id=?`, actor.HouseholdID, clientID).Scan(&personID); err != nil {
		return Event{}, ErrNotFound
	}
	events, err := s.query(ctx, actor, personID, `AND e.client_id=?`, clientID)
	if err != nil || len(events) != 1 {
		return Event{}, ErrNotFound
	}
	return events[0], nil
}

func (s *Service) query(ctx context.Context, actor policy.ActorScope, personID, tail string, args ...any) ([]Event, error) {
	if err := s.visiblePerson(ctx, actor, personID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.id,e.person_id,e.actor_user_id,COALESCE(u.email,''),e.type,e.source,e.client_id,e.occurred_at,e.ended_at,e.details_json FROM care_events e LEFT JOIN users u ON u.id=e.actor_user_id WHERE e.household_id=? AND e.person_id=? `+tail, append([]any{actor.HouseholdID, personID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var e Event
		var occurred, ended, raw string
		if err := rows.Scan(&e.ID, &e.PersonID, &e.ActorID, &e.ActorEmail, &e.Type, &e.Source, &e.ClientID, &occurred, &ended, &raw); err != nil {
			return nil, err
		}
		e.OccurredAt, _ = time.Parse(time.RFC3339, occurred)
		if ended != "" {
			e.EndedAt, _ = time.Parse(time.RFC3339, ended)
		}
		e.Details = map[string]string{}
		_ = json.Unmarshal([]byte(raw), &e.Details)
		events = append(events, e)
	}
	return events, rows.Err()
}

func (s *Service) visiblePerson(ctx context.Context, actor policy.ActorScope, personID string) error {
	if !actor.Valid() {
		return policy.ErrUnauthorized
	}
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM household_people p JOIN household_members m ON m.household_id=p.household_id AND m.user_id=? JOIN users u ON u.id=m.user_id AND u.status='active' WHERE p.id=? AND p.household_id=? AND p.archived=0 AND (p.visibility='shared' OR p.owner_user_id=?)`, actor.ActorID, personID, actor.HouseholdID, actor.ActorID).Scan(&one)
	if err != nil {
		return ErrNotFound
	}
	return nil
}

// Today summarises the day so far in the household's time zone.
type Today struct {
	LastFeed                   *Event
	Running                    *Event
	Feeds, Wet, Dirty, SpitUps int
	MedianGap                  time.Duration
	GapSamples                 int
}

// Summarise reads facts from entries: counts since local midnight, the last
// feed, a running feed, and the median gap between feed starts over the
// entries given (callers pass the last seven days).
func Summarise(events []Event, now time.Time) Today {
	var t Today
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var starts []time.Time
	for i := range events {
		e := events[i]
		if e.Type == Feed {
			starts = append(starts, e.OccurredAt)
			if t.LastFeed == nil || e.OccurredAt.After(t.LastFeed.OccurredAt) {
				t.LastFeed = &events[i]
			}
			if e.Running() && (t.Running == nil || e.OccurredAt.After(t.Running.OccurredAt)) {
				t.Running = &events[i]
			}
		}
		if e.OccurredAt.Before(midnight) {
			continue
		}
		switch e.Type {
		case Feed:
			t.Feeds++
		case Diaper:
			if e.Details["kind"] == "wet" || e.Details["kind"] == "both" {
				t.Wet++
			}
			if e.Details["kind"] == "dirty" || e.Details["kind"] == "both" {
				t.Dirty++
			}
		case SpitUp:
			t.SpitUps++
		}
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
	var gaps []time.Duration
	for i := 1; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap > 0 && gap < 12*time.Hour {
			gaps = append(gaps, gap)
		}
	}
	if len(gaps) > 0 {
		sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
		middle := len(gaps) / 2
		t.MedianGap = gaps[middle]
		if len(gaps)%2 == 0 {
			t.MedianGap = (gaps[middle-1] + gaps[middle]) / 2
		}
		t.GapSamples = len(gaps)
	}
	return t
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
