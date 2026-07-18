package health

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

type ScopeFilter string

const (
	AllRecords      ScopeFilter = "all"
	SharedRecords   ScopeFilter = "shared"
	PersonalRecords ScopeFilter = "personal"
)

var ErrInvalidRecord = errors.New("health record is invalid")

type Provenance struct {
	SourceID      string
	SourceFamily  string
	SourceVersion int64
	LocatorKind   string
	LocatorValue  string
	GeneratedBy   string
	Model         string
	PromptVersion string
	SchemaVersion string
}

type ObservationDraft struct {
	Visibility       policy.Visibility
	Subject          string
	Analyte          string
	Specimen         string
	Method           string
	ReferenceContext string
	ObservedOn       string
	Value            string
	Unit             string
	ReferenceLow     string
	ReferenceHigh    string
	ReferenceUnit    string
	Provenance       Provenance
}

type AppointmentDraft struct {
	Visibility  policy.Visibility
	Subject     string
	Label       string
	Provider    string
	Location    string
	ScheduledOn string
	Status      string
	Provenance  Provenance
}

type RoutineDraft struct {
	Visibility policy.Visibility
	Subject    string
	Label      string
	Cadence    string
	NextDueOn  string
	Status     string
	Provenance Provenance
}

type Observation struct {
	ID               string
	HouseholdID      string
	OwnerID          string
	Visibility       policy.Visibility
	Subject          string
	Analyte          string
	Specimen         string
	Method           string
	ReferenceContext string
	ComparabilityKey string
	ObservedOn       string
	Value            Value
	OriginalValue    string
	Unit             string
	ReferenceLow     *Value
	ReferenceHigh    *Value
	ReferenceUnit    string
	SourceID         string
	SourceFamily     string
	SourceVersion    int64
	LocatorKind      string
	LocatorValue     string
	GeneratedBy      string
	Model            string
	PromptVersion    string
	SchemaVersion    string
	DataRevision     int64
	SupersedesID     string
	Version          int64
	CreatedAt        time.Time
}

type Appointment struct {
	ID, HouseholdID, OwnerID                                string
	Visibility                                              policy.Visibility
	Subject, Label, Provider, Location, ScheduledOn, Status string
	SourceID, LocatorKind, LocatorValue                     string
}

type Routine struct {
	ID, HouseholdID, OwnerID                   string
	Visibility                                 policy.Visibility
	Subject, Label, Cadence, NextDueOn, Status string
	SourceID, LocatorKind, LocatorValue        string
}

type Series struct {
	Key              string
	Analyte          string
	Subject          string
	Specimen         string
	Method           string
	ReferenceContext string
	Unit             string
	Observations     []Observation
}

type Conflict struct {
	RecordID string
	Version  int64
	Analyte  string
	Reason   string
	SourceID string
}

type Summary struct {
	Observations []Observation
	Appointments []Appointment
	Routines     []Routine
	Series       []Series
	Conflicts    []Conflict
}

type Service struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB) *Service { return &Service{db: db, now: time.Now} }

func (s *Service) CreateObservation(ctx context.Context, actor policy.ActorScope, draft ObservationDraft) (Observation, error) {
	draft.Visibility = policy.PersonalDefault(draft.Visibility)
	observation, err := prepareObservation(actor, draft, s.now().UTC())
	if err != nil {
		return Observation{}, err
	}
	observation.ID, err = healthID()
	if err != nil {
		return Observation{}, ErrInvalidRecord
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Observation{}, err
	}
	defer tx.Rollback()
	if err := authorize(ctx, tx, actor); err != nil {
		return Observation{}, err
	}
	if err := setRevision(ctx, tx, actor, draft.Visibility, &observation.DataRevision); err != nil {
		return Observation{}, err
	}
	if err := insertObservation(ctx, tx, observation); err != nil {
		return Observation{}, err
	}
	if err := link(ctx, tx, "observation", observation.ID, actor, draft.Visibility, draft.Provenance, observation.Analyte+" "+observation.Subject); err != nil {
		return Observation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Observation{}, err
	}
	return observation, nil
}

func (s *Service) CorrectObservation(ctx context.Context, actor policy.ActorScope, id string, expectedVersion int64, value, unit string) (Observation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Observation{}, err
	}
	defer tx.Rollback()
	if err := authorize(ctx, tx, actor); err != nil {
		return Observation{}, err
	}
	current, err := findObservation(ctx, tx, actor, id)
	if err != nil {
		return Observation{}, err
	}
	if err := actor.CanMutate(policy.Resource{HouseholdID: current.HouseholdID, OwnerID: current.OwnerID, Visibility: current.Visibility, Version: current.Version}, expectedVersion); err != nil {
		return Observation{}, err
	}
	draft := ObservationDraft{Visibility: current.Visibility, Subject: current.Subject, Analyte: current.Analyte, Specimen: current.Specimen, Method: current.Method, ReferenceContext: current.ReferenceContext, ObservedOn: current.ObservedOn, Value: value, Unit: unit, ReferenceUnit: current.ReferenceUnit, Provenance: Provenance{SourceID: current.SourceID, SourceFamily: current.SourceFamily, SourceVersion: current.SourceVersion, LocatorKind: current.LocatorKind, LocatorValue: current.LocatorValue, GeneratedBy: "user", SchemaVersion: "health-v1"}}
	if current.ReferenceLow != nil {
		draft.ReferenceLow = current.ReferenceLow.PlainString()
	}
	if current.ReferenceHigh != nil {
		draft.ReferenceHigh = current.ReferenceHigh.PlainString()
	}
	replacement, err := prepareObservation(actor, draft, s.now().UTC())
	if err != nil {
		return Observation{}, err
	}
	replacement.ID, err = healthID()
	if err != nil {
		return Observation{}, ErrInvalidRecord
	}
	replacement.SupersedesID = current.ID
	result, err := tx.ExecContext(ctx, `UPDATE health_observations SET active=0,version=version+1,updated_at=? WHERE id=? AND active=1 AND version=?`, s.now().UTC().Format(time.RFC3339Nano), id, expectedVersion)
	if err != nil {
		return Observation{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Observation{}, policy.ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM search_entries WHERE record_family='health' AND record_id=?`, id); err != nil {
		return Observation{}, err
	}
	if err := setRevision(ctx, tx, actor, current.Visibility, &replacement.DataRevision); err != nil {
		return Observation{}, err
	}
	if err := insertObservation(ctx, tx, replacement); err != nil {
		return Observation{}, err
	}
	if err := link(ctx, tx, "observation", replacement.ID, actor, current.Visibility, draft.Provenance, replacement.Analyte+" "+replacement.Subject); err != nil {
		return Observation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Observation{}, err
	}
	return replacement, nil
}

func (s *Service) CreateAppointment(ctx context.Context, actor policy.ActorScope, draft AppointmentDraft) (Appointment, error) {
	draft.Visibility = policy.PersonalDefault(draft.Visibility)
	if !actor.Valid() || strings.TrimSpace(draft.Subject) == "" || strings.TrimSpace(draft.Label) == "" || !validDate(draft.ScheduledOn) || !validProvenance(draft.Provenance) {
		return Appointment{}, ErrInvalidRecord
	}
	if draft.Status == "" {
		draft.Status = "planned"
	}
	if draft.Status != "planned" && draft.Status != "completed" && draft.Status != "cancelled" {
		return Appointment{}, ErrInvalidRecord
	}
	id, err := healthID()
	if err != nil {
		return Appointment{}, ErrInvalidRecord
	}
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Appointment{}, err
	}
	defer tx.Rollback()
	if err := authorize(ctx, tx, actor); err != nil {
		return Appointment{}, err
	}
	var revision int64
	if err := setRevision(ctx, tx, actor, draft.Visibility, &revision); err != nil {
		return Appointment{}, err
	}
	p := draft.Provenance
	generated := defaultGenerated(p.GeneratedBy)
	schema := defaultSchema(p.SchemaVersion)
	_, err = tx.ExecContext(ctx, `INSERT INTO health_appointments(id,household_id,owner_user_id,visibility,source_id,source_family,source_version,subject,label,provider,location,scheduled_on,status,generated_by,model,prompt_version,schema_version,data_revision,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`, id, actor.HouseholdID, actor.ActorID, draft.Visibility, p.SourceID, p.SourceFamily, p.SourceVersion, strings.TrimSpace(draft.Subject), strings.TrimSpace(draft.Label), strings.TrimSpace(draft.Provider), strings.TrimSpace(draft.Location), draft.ScheduledOn, draft.Status, generated, p.Model, p.PromptVersion, schema, revision, stamp, stamp)
	if err != nil {
		return Appointment{}, err
	}
	if err := link(ctx, tx, "appointment", id, actor, draft.Visibility, p, draft.Label+" "+draft.Subject); err != nil {
		return Appointment{}, err
	}
	if err := tx.Commit(); err != nil {
		return Appointment{}, err
	}
	return Appointment{ID: id, HouseholdID: actor.HouseholdID, OwnerID: actor.ActorID, Visibility: draft.Visibility, Subject: draft.Subject, Label: draft.Label, Provider: draft.Provider, Location: draft.Location, ScheduledOn: draft.ScheduledOn, Status: draft.Status, SourceID: p.SourceID, LocatorKind: p.LocatorKind, LocatorValue: p.LocatorValue}, nil
}

func (s *Service) CreateRoutine(ctx context.Context, actor policy.ActorScope, draft RoutineDraft) (Routine, error) {
	draft.Visibility = policy.PersonalDefault(draft.Visibility)
	if !actor.Valid() || strings.TrimSpace(draft.Subject) == "" || strings.TrimSpace(draft.Label) == "" || strings.TrimSpace(draft.Cadence) == "" || !validDate(draft.NextDueOn) || !validProvenance(draft.Provenance) {
		return Routine{}, ErrInvalidRecord
	}
	if draft.Status == "" {
		draft.Status = "active"
	}
	if draft.Status != "active" && draft.Status != "paused" && draft.Status != "completed" {
		return Routine{}, ErrInvalidRecord
	}
	id, err := healthID()
	if err != nil {
		return Routine{}, ErrInvalidRecord
	}
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Routine{}, err
	}
	defer tx.Rollback()
	if err := authorize(ctx, tx, actor); err != nil {
		return Routine{}, err
	}
	var revision int64
	if err := setRevision(ctx, tx, actor, draft.Visibility, &revision); err != nil {
		return Routine{}, err
	}
	p := draft.Provenance
	generated := defaultGenerated(p.GeneratedBy)
	schema := defaultSchema(p.SchemaVersion)
	_, err = tx.ExecContext(ctx, `INSERT INTO health_care_routines(id,household_id,owner_user_id,visibility,source_id,source_family,source_version,subject,label,cadence,next_due_on,status,generated_by,model,prompt_version,schema_version,data_revision,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`, id, actor.HouseholdID, actor.ActorID, draft.Visibility, p.SourceID, p.SourceFamily, p.SourceVersion, strings.TrimSpace(draft.Subject), strings.TrimSpace(draft.Label), strings.TrimSpace(draft.Cadence), draft.NextDueOn, draft.Status, generated, p.Model, p.PromptVersion, schema, revision, stamp, stamp)
	if err != nil {
		return Routine{}, err
	}
	if err := link(ctx, tx, "routine", id, actor, draft.Visibility, p, draft.Label+" "+draft.Subject); err != nil {
		return Routine{}, err
	}
	if err := tx.Commit(); err != nil {
		return Routine{}, err
	}
	return Routine{ID: id, HouseholdID: actor.HouseholdID, OwnerID: actor.ActorID, Visibility: draft.Visibility, Subject: draft.Subject, Label: draft.Label, Cadence: draft.Cadence, NextDueOn: draft.NextDueOn, Status: draft.Status, SourceID: p.SourceID, LocatorKind: p.LocatorKind, LocatorValue: p.LocatorValue}, nil
}

func (s *Service) Summarize(ctx context.Context, actor policy.ActorScope, filter ScopeFilter) (Summary, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Summary{}, err
	}
	defer tx.Rollback()
	if err := authorize(ctx, tx, actor); err != nil {
		return Summary{}, err
	}
	if filter != SharedRecords && filter != PersonalRecords {
		filter = AllRecords
	}
	scopeSQL, args := scopeClause(actor, filter)
	observations, err := queryObservations(ctx, tx, scopeSQL, args)
	if err != nil {
		return Summary{}, err
	}
	appointments, err := queryAppointments(ctx, tx, scopeSQL, args)
	if err != nil {
		return Summary{}, err
	}
	routines, err := queryRoutines(ctx, tx, scopeSQL, args)
	if err != nil {
		return Summary{}, err
	}
	if err := tx.Commit(); err != nil {
		return Summary{}, err
	}
	summary := Summary{Observations: observations, Appointments: appointments, Routines: routines}
	summary.Series, summary.Conflicts = buildSeries(observations)
	sort.Slice(summary.Appointments, func(i, j int) bool { return summary.Appointments[i].ScheduledOn < summary.Appointments[j].ScheduledOn })
	sort.Slice(summary.Routines, func(i, j int) bool { return summary.Routines[i].NextDueOn < summary.Routines[j].NextDueOn })
	return summary, nil
}

func prepareObservation(actor policy.ActorScope, draft ObservationDraft, now time.Time) (Observation, error) {
	if !actor.Valid() || strings.TrimSpace(draft.Subject) == "" || strings.TrimSpace(draft.Analyte) == "" || !validDate(draft.ObservedOn) || !validProvenance(draft.Provenance) {
		return Observation{}, ErrInvalidRecord
	}
	value, err := ParseValue(draft.Value)
	if err != nil {
		return Observation{}, ErrInvalidRecord
	}
	unit, err := UnitFor(draft.Unit)
	if err != nil {
		return Observation{}, ErrInvalidRecord
	}
	key, err := ComparabilityKey(draft.Analyte, draft.Subject, draft.Specimen, draft.Method, draft.ReferenceContext, draft.Unit)
	if err != nil {
		return Observation{}, ErrInvalidRecord
	}
	p := draft.Provenance
	o := Observation{HouseholdID: actor.HouseholdID, OwnerID: actor.ActorID, Visibility: draft.Visibility, Subject: strings.TrimSpace(draft.Subject), Analyte: strings.TrimSpace(draft.Analyte), Specimen: strings.TrimSpace(draft.Specimen), Method: strings.TrimSpace(draft.Method), ReferenceContext: strings.TrimSpace(draft.ReferenceContext), ComparabilityKey: key, ObservedOn: draft.ObservedOn, Value: value, OriginalValue: strings.TrimSpace(draft.Value), Unit: strings.TrimSpace(draft.Unit), ReferenceUnit: strings.TrimSpace(draft.ReferenceUnit), SourceID: p.SourceID, SourceFamily: p.SourceFamily, SourceVersion: p.SourceVersion, LocatorKind: p.LocatorKind, LocatorValue: p.LocatorValue, GeneratedBy: defaultGenerated(p.GeneratedBy), Model: p.Model, PromptVersion: p.PromptVersion, SchemaVersion: defaultSchema(p.SchemaVersion), Version: 1, CreatedAt: now}
	_ = unit
	if draft.ReferenceLow != "" {
		v, e := ParseValue(draft.ReferenceLow)
		if e != nil {
			return Observation{}, ErrInvalidRecord
		}
		o.ReferenceLow = &v
	}
	if draft.ReferenceHigh != "" {
		v, e := ParseValue(draft.ReferenceHigh)
		if e != nil {
			return Observation{}, ErrInvalidRecord
		}
		o.ReferenceHigh = &v
	}
	if (o.ReferenceLow != nil || o.ReferenceHigh != nil) && o.ReferenceUnit == "" {
		o.ReferenceUnit = o.Unit
	}
	return o, nil
}

func insertObservation(ctx context.Context, tx *sql.Tx, o Observation) error {
	var lowC, lowS, highC, highS any
	if o.ReferenceLow != nil {
		lowC = o.ReferenceLow.Coefficient
		lowS = o.ReferenceLow.Scale
	}
	if o.ReferenceHigh != nil {
		highC = o.ReferenceHigh.Coefficient
		highS = o.ReferenceHigh.Scale
	}
	stamp := o.CreatedAt.Format(time.RFC3339Nano)
	_, err := tx.ExecContext(ctx, `INSERT INTO health_observations(id,household_id,owner_user_id,visibility,source_id,source_family,source_version,subject,analyte,specimen,method,reference_context,comparability_key,observed_on,value_coefficient,value_scale,value_original,unit,reference_low_coefficient,reference_low_scale,reference_high_coefficient,reference_high_scale,reference_unit,generated_by,model,prompt_version,schema_version,data_revision,supersedes_id,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, o.ID, o.HouseholdID, o.OwnerID, o.Visibility, o.SourceID, o.SourceFamily, o.SourceVersion, o.Subject, o.Analyte, o.Specimen, o.Method, o.ReferenceContext, o.ComparabilityKey, o.ObservedOn, o.Value.Coefficient, o.Value.Scale, o.OriginalValue, o.Unit, lowC, lowS, highC, highS, o.ReferenceUnit, o.GeneratedBy, o.Model, o.PromptVersion, o.SchemaVersion, o.DataRevision, nullable(o.SupersedesID), o.Version, stamp, stamp)
	return err
}

func link(ctx context.Context, tx *sql.Tx, kind, id string, actor policy.ActorScope, visibility policy.Visibility, p Provenance, content string) error {
	evidenceID, err := healthID()
	if err != nil {
		return ErrInvalidRecord
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO evidence_links(id,record_family,record_id,household_id,owner_user_id,visibility,source_id,source_family,source_version,locator_kind,locator_value,created_at) VALUES(?,'health',?,?,?,?,?,?,?,?,?,?)`, evidenceID, id, actor.HouseholdID, actor.ActorID, visibility, p.SourceID, p.SourceFamily, p.SourceVersion, p.LocatorKind, p.LocatorValue, stamp)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO search_entries(record_family,record_id,household_id,owner_user_id,visibility,content) VALUES('health',?,?,?,?,?)`, id, actor.HouseholdID, actor.ActorID, visibility, strings.TrimSpace(content+" "+kind))
	return err
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func authorize(ctx context.Context, q queryer, a policy.ActorScope) error {
	if !a.Valid() {
		return policy.ErrUnauthorized
	}
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM household_members m JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id WHERE m.household_id=? AND m.user_id=? AND u.status='active' AND h.status='active'`, a.HouseholdID, a.ActorID).Scan(&one)
	if err != nil {
		return policy.ErrUnauthorized
	}
	return nil
}
func setRevision(ctx context.Context, tx *sql.Tx, a policy.ActorScope, v policy.Visibility, target *int64) error {
	if v == policy.Shared {
		return tx.QueryRowContext(ctx, `SELECT shared_revision FROM household_revisions WHERE household_id=?`, a.HouseholdID).Scan(target)
	}
	return tx.QueryRowContext(ctx, `SELECT personal_revision FROM user_revisions WHERE user_id=?`, a.ActorID).Scan(target)
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
func validDate(v string) bool {
	if len(v) != 10 {
		return false
	}
	_, e := time.Parse("2006-01-02", v)
	return e == nil
}
func defaultGenerated(v string) string {
	if v == "" {
		return "application"
	}
	return v
}
func defaultSchema(v string) string {
	if v == "" {
		return "health-v1"
	}
	return v
}
func healthID() (string, error) {
	var raw [16]byte
	if _, e := rand.Read(raw[:]); e != nil {
		return "", e
	}
	return hex.EncodeToString(raw[:]), nil
}
func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func scopeClause(a policy.ActorScope, f ScopeFilter) (string, []any) {
	where := `household_id=? AND active=1 AND (visibility='shared' OR owner_user_id=?)`
	args := []any{a.HouseholdID, a.ActorID}
	if f == SharedRecords {
		where += ` AND visibility='shared'`
	} else if f == PersonalRecords {
		where += ` AND visibility='personal' AND owner_user_id=?`
		args = append(args, a.ActorID)
	}
	return where, args
}

func queryObservations(ctx context.Context, tx *sql.Tx, where string, args []any) ([]Observation, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,household_id,owner_user_id,visibility,subject,analyte,specimen,method,reference_context,comparability_key,observed_on,value_coefficient,value_scale,value_original,unit,reference_low_coefficient,reference_low_scale,reference_high_coefficient,reference_high_scale,reference_unit,source_id,source_family,source_version,COALESCE((SELECT locator_kind FROM evidence_links e WHERE e.record_family='health' AND e.record_id=health_observations.id LIMIT 1),''),COALESCE((SELECT locator_value FROM evidence_links e WHERE e.record_family='health' AND e.record_id=health_observations.id LIMIT 1),''),generated_by,model,prompt_version,schema_version,data_revision,COALESCE(supersedes_id,''),version,created_at FROM health_observations WHERE `+where+` ORDER BY observed_on,id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Observation
	for rows.Next() {
		o, e := scanObservation(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
func findObservation(ctx context.Context, q queryer, a policy.ActorScope, id string) (Observation, error) {
	row := q.QueryRowContext(ctx, `SELECT id,household_id,owner_user_id,visibility,subject,analyte,specimen,method,reference_context,comparability_key,observed_on,value_coefficient,value_scale,value_original,unit,reference_low_coefficient,reference_low_scale,reference_high_coefficient,reference_high_scale,reference_unit,source_id,source_family,source_version,COALESCE((SELECT locator_kind FROM evidence_links e WHERE e.record_family='health' AND e.record_id=health_observations.id LIMIT 1),''),COALESCE((SELECT locator_value FROM evidence_links e WHERE e.record_family='health' AND e.record_id=health_observations.id LIMIT 1),''),generated_by,model,prompt_version,schema_version,data_revision,COALESCE(supersedes_id,''),version,created_at FROM health_observations WHERE id=? AND active=1 AND household_id=? AND (visibility='shared' OR owner_user_id=?)`, id, a.HouseholdID, a.ActorID)
	o, e := scanObservation(row)
	if errors.Is(e, sql.ErrNoRows) {
		return Observation{}, policy.ErrUnauthorized
	}
	return o, e
}

type scanner interface{ Scan(...any) error }

func scanObservation(row scanner) (Observation, error) {
	var o Observation
	var visibility, created string
	var lowC, lowS, highC, highS sql.NullInt64
	err := row.Scan(&o.ID, &o.HouseholdID, &o.OwnerID, &visibility, &o.Subject, &o.Analyte, &o.Specimen, &o.Method, &o.ReferenceContext, &o.ComparabilityKey, &o.ObservedOn, &o.Value.Coefficient, &o.Value.Scale, &o.OriginalValue, &o.Unit, &lowC, &lowS, &highC, &highS, &o.ReferenceUnit, &o.SourceID, &o.SourceFamily, &o.SourceVersion, &o.LocatorKind, &o.LocatorValue, &o.GeneratedBy, &o.Model, &o.PromptVersion, &o.SchemaVersion, &o.DataRevision, &o.SupersedesID, &o.Version, &created)
	if err != nil {
		return Observation{}, err
	}
	o.Visibility = policy.Visibility(visibility)
	if lowC.Valid {
		v := Value{Coefficient: lowC.Int64, Scale: uint8(lowS.Int64)}
		o.ReferenceLow = &v
	}
	if highC.Valid {
		v := Value{Coefficient: highC.Int64, Scale: uint8(highS.Int64)}
		o.ReferenceHigh = &v
	}
	o.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return o, nil
}

func queryAppointments(ctx context.Context, tx *sql.Tx, where string, args []any) ([]Appointment, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,household_id,owner_user_id,visibility,subject,label,provider,location,scheduled_on,status,source_id,COALESCE((SELECT locator_kind FROM evidence_links e WHERE e.record_family='health' AND e.record_id=health_appointments.id LIMIT 1),''),COALESCE((SELECT locator_value FROM evidence_links e WHERE e.record_family='health' AND e.record_id=health_appointments.id LIMIT 1),'') FROM health_appointments WHERE `+where+` AND status='planned'`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Appointment
	for rows.Next() {
		var v string
		var a Appointment
		if err := rows.Scan(&a.ID, &a.HouseholdID, &a.OwnerID, &v, &a.Subject, &a.Label, &a.Provider, &a.Location, &a.ScheduledOn, &a.Status, &a.SourceID, &a.LocatorKind, &a.LocatorValue); err != nil {
			return nil, err
		}
		a.Visibility = policy.Visibility(v)
		out = append(out, a)
	}
	return out, rows.Err()
}
func queryRoutines(ctx context.Context, tx *sql.Tx, where string, args []any) ([]Routine, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,household_id,owner_user_id,visibility,subject,label,cadence,next_due_on,status,source_id,COALESCE((SELECT locator_kind FROM evidence_links e WHERE e.record_family='health' AND e.record_id=health_care_routines.id LIMIT 1),''),COALESCE((SELECT locator_value FROM evidence_links e WHERE e.record_family='health' AND e.record_id=health_care_routines.id LIMIT 1),'') FROM health_care_routines WHERE `+where+` AND status='active'`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Routine
	for rows.Next() {
		var v string
		var r Routine
		if err := rows.Scan(&r.ID, &r.HouseholdID, &r.OwnerID, &v, &r.Subject, &r.Label, &r.Cadence, &r.NextDueOn, &r.Status, &r.SourceID, &r.LocatorKind, &r.LocatorValue); err != nil {
			return nil, err
		}
		r.Visibility = policy.Visibility(v)
		out = append(out, r)
	}
	return out, rows.Err()
}

func buildSeries(observations []Observation) ([]Series, []Conflict) {
	baseFamilies := map[string]map[string]struct{}{}
	for _, o := range observations {
		u, _ := UnitFor(o.Unit)
		base := strings.ToLower(strings.Join([]string{o.Analyte, o.Subject, o.Specimen, o.Method, o.ReferenceContext}, "\x1f"))
		if baseFamilies[base] == nil {
			baseFamilies[base] = map[string]struct{}{}
		}
		baseFamilies[base][u.Family] = struct{}{}
	}
	grouped := map[string][]Observation{}
	var conflicts []Conflict
	for _, o := range observations {
		u, _ := UnitFor(o.Unit)
		base := strings.ToLower(strings.Join([]string{o.Analyte, o.Subject, o.Specimen, o.Method, o.ReferenceContext}, "\x1f"))
		if len(baseFamilies[base]) > 1 {
			conflicts = append(conflicts, Conflict{RecordID: o.ID, Version: o.Version, Analyte: o.Analyte, Reason: "Units are not explicitly compatible; enter the correct value and unit.", SourceID: o.SourceID})
			continue
		}
		canonical, err := Convert(o.Value, o.Unit, u.Canonical)
		if err != nil {
			conflicts = append(conflicts, Conflict{RecordID: o.ID, Version: o.Version, Analyte: o.Analyte, Reason: "Unit needs a user-supplied value and unit.", SourceID: o.SourceID})
			continue
		}
		o.Value = canonical
		o.Unit = u.Canonical
		grouped[o.ComparabilityKey] = append(grouped[o.ComparabilityKey], o)
	}
	keys := make([]string, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	series := make([]Series, 0, len(keys))
	for _, key := range keys {
		items := grouped[key]
		sort.Slice(items, func(i, j int) bool { return items[i].ObservedOn < items[j].ObservedOn })
		first := items[0]
		series = append(series, Series{Key: key, Analyte: first.Analyte, Subject: first.Subject, Specimen: first.Specimen, Method: first.Method, ReferenceContext: first.ReferenceContext, Unit: first.Unit, Observations: items})
	}
	return series, conflicts
}
