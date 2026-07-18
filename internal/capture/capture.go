// Package capture stages natural-language capture and commits only typed domain proposals.
package capture

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/glnarayanan/mithra/internal/finance"
	"github.com/glnarayanan/mithra/internal/health"
	"github.com/glnarayanan/mithra/internal/planning"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/storage"
)

var (
	ErrInvalidProposal = errors.New("capture proposal is invalid")
	ErrNotFound        = errors.New("capture is unavailable")
	ErrUndoRefused     = errors.New("capture undo requires impact-aware deletion")
)

type Variant string

const (
	FinanceVariant  Variant = "finance"
	HealthVariant   Variant = "health"
	PlanningVariant Variant = "planning"
)

type Proposal struct {
	Variant  Variant
	Finance  *FinanceProposal
	Health   *HealthProposal
	Planning *PlanningProposal
}

type FinanceProposal struct {
	Kind                                        finance.Kind
	Label, Category, Date, EndDate, Status      string
	AmountText, IncompleteNote, CurrencyContext string
}
type HealthKind string

const (
	Observation HealthKind = "observation"
	Appointment HealthKind = "appointment"
	Routine     HealthKind = "routine"
)

type HealthProposal struct {
	Kind                                                                                                          HealthKind
	Subject, Label, Analyte, ObservedOn, Value, Unit, Provider, Location, ScheduledOn, Cadence, NextDueOn, Status string
}
type PlanningProposal struct {
	Title, Description, Location, StartsOn, EndsOn, StartsAt, EndsAt, Timezone, Status string
	AllDay                                                                             bool
}
type TextRequest struct {
	Text       string
	Summary    string
	Visibility policy.Visibility
	Proposal   Proposal
}
type AudioRequest struct {
	Bytes      []byte
	Visibility policy.Visibility
}
type Capture struct {
	ID, SourceID, RawAudioSourceID, Summary, State, ClarificationField, ClarificationQuestion, RecordFamily, RecordID, AudioState string
	Visibility                                                                                                                    policy.Visibility
	UndoUntil                                                                                                                     time.Time
}

type Service struct {
	db       *sql.DB
	sources  *storage.Service
	finance  *finance.Service
	health   *health.Service
	planning *planning.Service
	now      func() time.Time
}

func New(db *sql.DB, sources *storage.Service) *Service {
	return &Service{db: db, sources: sources, finance: finance.New(db), health: health.New(db), planning: planning.New(db), now: time.Now}
}

func (s *Service) SubmitText(ctx context.Context, actor policy.ActorScope, request TextRequest) (Capture, error) {
	if err := validRequest(actor, request); err != nil || s == nil || s.sources == nil {
		return Capture{}, ErrInvalidProposal
	}
	id, err := captureID()
	if err != nil {
		return Capture{}, err
	}
	source, err := s.sources.Store(ctx, actor, []byte(request.Text), storage.Metadata{Family: "text", Version: 1, Visibility: request.Visibility, LocatorKind: "source", LocatorValue: id})
	if err != nil {
		return Capture{}, err
	}
	receipt, err := s.commit(ctx, actor, id, source, "text", "", request)
	if err != nil {
		_ = s.sources.Delete(ctx, actor, source.ID)
	}
	return receipt, err
}

// StageAudio persists bytes only in the encrypted U3 source store. It returns no storage key or plaintext path.
func (s *Service) StageAudio(ctx context.Context, actor policy.ActorScope, request AudioRequest) (Capture, error) {
	if s == nil || s.sources == nil || !actor.Valid() || !visibility(request.Visibility) || len(request.Bytes) == 0 {
		return Capture{}, ErrInvalidProposal
	}
	id, err := captureID()
	if err != nil {
		return Capture{}, err
	}
	raw, err := s.sources.Store(ctx, actor, request.Bytes, storage.Metadata{Family: "voice", Version: 1, Visibility: request.Visibility, LocatorKind: "source", LocatorValue: id})
	if err != nil {
		return Capture{}, err
	}
	now := s.now().UTC()
	_, err = s.db.ExecContext(ctx, `INSERT INTO captures(id,household_id,owner_user_id,visibility,raw_audio_source_id,source_kind,state,audio_state,audio_attempts,cleanup_at,created_at,updated_at) VALUES(?,?,?,?,?,'audio','processing','retryable',0,?,?,?)`, id, actor.HouseholdID, actor.ActorID, request.Visibility, raw.ID, now.Add(15*time.Minute).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		_ = s.sources.Delete(ctx, actor, raw.ID)
		return Capture{}, err
	}
	return Capture{ID: id, RawAudioSourceID: raw.ID, State: "processing", Visibility: request.Visibility, AudioState: "retryable"}, nil
}

func (s *Service) SubmitTranscript(ctx context.Context, actor policy.ActorScope, captureID string, request TextRequest) (Capture, error) {
	if err := validRequest(actor, request); err != nil {
		return Capture{}, ErrInvalidProposal
	}
	row, err := s.load(ctx, actor, captureID)
	if err != nil || row.raw == "" || row.audio != "retryable" || row.state != "processing" {
		return Capture{}, ErrNotFound
	}
	request.Visibility = row.visibility
	source, err := s.sources.Store(ctx, actor, []byte(request.Text), storage.Metadata{Family: "text", Version: 1, Visibility: request.Visibility, LocatorKind: "source", LocatorValue: captureID})
	if err != nil {
		return Capture{}, err
	}
	receipt, err := s.commit(ctx, actor, captureID, source, "transcript", row.raw, request)
	if err != nil {
		_ = s.sources.Delete(ctx, actor, source.ID)
	}
	return receipt, err
}

// Get returns an actor's own capture receipt; shared records remain available through their domain lens.
func (s *Service) Get(ctx context.Context, actor policy.ActorScope, id string) (Capture, error) {
	r, err := s.load(ctx, actor, id)
	if err != nil {
		return Capture{}, err
	}
	return r.capture(id), nil
}

// List returns the actor's recent capture receipts, bounded for the HTTP view.
func (s *Service) List(ctx context.Context, actor policy.ActorScope, limit int) ([]Capture, error) {
	if !actor.Valid() || limit < 1 || limit > 100 {
		return nil, ErrInvalidProposal
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM captures WHERE household_id=? AND owner_user_id=? ORDER BY created_at DESC LIMIT ?`, actor.HouseholdID, actor.ActorID, limit)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var out []Capture
	for _, id := range ids {
		r, e := s.load(ctx, actor, id)
		if e != nil {
			return nil, e
		}
		out = append(out, r.capture(id))
	}
	return out, nil
}

// AnswerClarification changes only the requested missing field. It either commits the typed proposal or asks one next question.
func (s *Service) AnswerClarification(ctx context.Context, actor policy.ActorScope, id, answer string) (Capture, error) {
	r, err := s.load(ctx, actor, id)
	if err != nil || r.state != "clarification" || r.source == "" || !safe(answer, 256) || strings.TrimSpace(answer) == "" {
		return Capture{}, ErrInvalidProposal
	}
	var request TextRequest
	if err := json.Unmarshal([]byte(r.proposal), &request.Proposal); err != nil {
		return Capture{}, ErrInvalidProposal
	}
	request.Visibility = r.visibility
	switch r.field {
	case "owner":
		if request.Proposal.Health == nil {
			return Capture{}, ErrInvalidProposal
		}
		request.Proposal.Health.Subject = strings.TrimSpace(answer)
	case "date":
		if request.Proposal.Finance != nil {
			request.Proposal.Finance.Date = strings.TrimSpace(answer)
		} else if request.Proposal.Health != nil {
			switch request.Proposal.Health.Kind {
			case Observation:
				request.Proposal.Health.ObservedOn = strings.TrimSpace(answer)
			case Appointment:
				request.Proposal.Health.ScheduledOn = strings.TrimSpace(answer)
			case Routine:
				request.Proposal.Health.NextDueOn = strings.TrimSpace(answer)
			}
		} else if request.Proposal.Planning != nil {
			if request.Proposal.Planning.AllDay {
				request.Proposal.Planning.StartsOn = strings.TrimSpace(answer)
			} else if request.Proposal.Planning.StartsAt == "" {
				request.Proposal.Planning.StartsAt = strings.TrimSpace(answer)
			} else {
				request.Proposal.Planning.EndsAt = strings.TrimSpace(answer)
			}
		}
	case "unit":
		if request.Proposal.Health == nil || request.Proposal.Health.Kind != Observation {
			return Capture{}, ErrInvalidProposal
		}
		request.Proposal.Health.Unit = strings.TrimSpace(answer)
	case "status":
		if request.Proposal.Finance != nil && request.Proposal.Finance.Kind == finance.Obligation {
			request.Proposal.Finance.Status = strings.TrimSpace(answer)
		} else if request.Proposal.Health != nil {
			request.Proposal.Health.Status = strings.TrimSpace(answer)
		} else if request.Proposal.Planning != nil {
			request.Proposal.Planning.Status = strings.TrimSpace(answer)
		} else {
			return Capture{}, ErrInvalidProposal
		}
	default:
		return Capture{}, ErrInvalidProposal
	}
	plaintext, source, err := s.sources.Read(ctx, actor, r.source)
	if err != nil {
		return Capture{}, err
	}
	conversation := make([]byte, 0, len(plaintext)+len(r.question)+len(answer)+40)
	conversation = append(conversation, plaintext...)
	conversation = append(conversation, "\n\nMithra asked: "...)
	conversation = append(conversation, r.question...)
	conversation = append(conversation, "\nUser answered: "...)
	conversation = append(conversation, strings.TrimSpace(answer)...)
	clear(plaintext)
	revisedSource, err := s.sources.Store(ctx, actor, conversation, storage.Metadata{Family: "text", Version: source.Version + 1, Visibility: r.visibility, LocatorKind: "source", LocatorValue: id})
	clear(conversation)
	if err != nil {
		return Capture{}, err
	}
	request.Text, request.Summary = "", r.summary // The evidence source already holds the original text; no user field is reused.
	receipt, err := s.commit(ctx, actor, id, revisedSource, r.kind, r.raw, request)
	if err != nil {
		_ = s.sources.Delete(ctx, actor, revisedSource.ID)
		return Capture{}, err
	}
	_ = s.sources.Delete(ctx, actor, source.ID)
	return receipt, nil
}

func (s *Service) commit(ctx context.Context, actor policy.ActorScope, id string, source storage.Source, kind, raw string, request TextRequest) (Capture, error) {
	existing, err := s.exists(ctx, actor, id)
	if err != nil {
		return Capture{}, err
	}
	var cleanupAt any
	if raw != "" {
		cleanupAt = s.now().UTC().Add(15 * time.Minute).Format(time.RFC3339Nano)
	}
	field, question := clarification(request.Proposal)
	now := s.now().UTC()
	if field != "" {
		if !existing {
			proposal, _ := json.Marshal(request.Proposal)
			_, err := s.db.ExecContext(ctx, `INSERT INTO captures(id,household_id,owner_user_id,visibility,source_id,source_kind,summary,state,clarification_field,clarification_question,proposal_json,audio_state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'clarification',?,?,?,'none',?,?)`, id, actor.HouseholdID, actor.ActorID, request.Visibility, source.ID, kind, summary(request), field, question, proposal, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
			if err != nil {
				return Capture{}, err
			}
		} else {
			proposal, _ := json.Marshal(request.Proposal)
			if _, err := s.db.ExecContext(ctx, `UPDATE captures SET source_id=?,source_kind=?,summary=?,state='clarification',clarification_field=?,clarification_question=?,proposal_json=?,cleanup_at=?,updated_at=? WHERE id=? AND household_id=? AND owner_user_id=?`, source.ID, kind, summary(request), field, question, proposal, cleanupAt, now.Format(time.RFC3339Nano), id, actor.HouseholdID, actor.ActorID); err != nil {
				return Capture{}, err
			}
		}
		return Capture{ID: id, SourceID: source.ID, RawAudioSourceID: raw, Summary: summary(request), State: "clarification", ClarificationField: field, ClarificationQuestion: question, Visibility: request.Visibility, AudioState: audioState(raw)}, nil
	}
	family, table, recordID, version, err := s.create(ctx, actor, request, source)
	if err != nil {
		return Capture{}, err
	}
	revision, err := s.revision(ctx, actor, request.Visibility)
	if err != nil {
		s.rollbackDerived(ctx, table, family, recordID, actor.HouseholdID, version)
		return Capture{}, err
	}
	undo := now.Add(10 * time.Minute)
	if !existing {
		_, err = s.db.ExecContext(ctx, `INSERT INTO captures(id,household_id,owner_user_id,visibility,source_id,source_kind,summary,state,record_family,record_table,record_id,record_version,undo_revision,undo_until,audio_state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'awaiting_confirmation',?,?,?,?,?,?,'none',?,?)`, id, actor.HouseholdID, actor.ActorID, request.Visibility, source.ID, kind, summary(request), family, table, recordID, version, revision, undo.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	} else {
		_, err = s.db.ExecContext(ctx, `UPDATE captures SET source_id=?,source_kind=?,summary=?,state='awaiting_confirmation',clarification_field='',clarification_question='',proposal_json='',record_family=?,record_table=?,record_id=?,record_version=?,undo_revision=?,undo_until=?,cleanup_at=?,updated_at=? WHERE id=? AND household_id=? AND owner_user_id=?`, source.ID, kind, summary(request), family, table, recordID, version, revision, undo.Format(time.RFC3339Nano), cleanupAt, now.Format(time.RFC3339Nano), id, actor.HouseholdID, actor.ActorID)
	}
	if err != nil {
		s.rollbackDerived(ctx, table, family, recordID, actor.HouseholdID, version)
		return Capture{}, err
	}
	return Capture{ID: id, SourceID: source.ID, RawAudioSourceID: raw, Summary: summary(request), State: "awaiting_confirmation", RecordFamily: family, RecordID: recordID, Visibility: request.Visibility, UndoUntil: undo, AudioState: audioState(raw)}, nil
}

func (s *Service) exists(ctx context.Context, actor policy.ActorScope, id string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM captures WHERE id=? AND household_id=? AND owner_user_id=?`, id, actor.HouseholdID, actor.ActorID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Service) rollbackDerived(ctx context.Context, table, family, id, householdID string, version int64) {
	if !validTable(table, family) || id == "" || version < 1 {
		return
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE `+table+` SET active=0,version=version+1,updated_at=? WHERE id=? AND household_id=? AND active=1 AND version=?`, s.now().UTC().Format(time.RFC3339Nano), id, householdID, version); err != nil {
		return
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM search_entries WHERE record_family=? AND record_id=?`, family, id); err != nil {
		return
	}
	_ = tx.Commit()
}

func (s *Service) Confirm(ctx context.Context, actor policy.ActorScope, id string) error {
	c, err := s.load(ctx, actor, id)
	if err != nil || c.state != "awaiting_confirmation" || c.recordID == "" {
		return ErrNotFound
	}
	now := s.now().UTC()
	if c.raw != "" {
		if err := s.sources.Delete(ctx, actor, c.raw); err != nil && !errors.Is(err, storage.ErrNotFound) {
			return err
		}
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE captures SET state='confirmed',audio_state=CASE WHEN raw_audio_source_id IS NULL THEN 'none' ELSE 'cleaned' END,cleanup_at=NULL,raw_audio_source_id=NULL,updated_at=? WHERE id=? AND household_id=? AND owner_user_id=?`, now.Format(time.RFC3339Nano), id, actor.HouseholdID, actor.ActorID); err != nil {
		return err
	}
	return nil
}

func (s *Service) FailAudio(ctx context.Context, actor policy.ActorScope, id string, terminal bool) error {
	c, err := s.load(ctx, actor, id)
	if err != nil || c.raw == "" || c.audio != "retryable" {
		return ErrNotFound
	}
	now := s.now().UTC()
	state, deadline := "retryable", now.Add(15*time.Minute)
	if terminal || c.attempts+1 >= 3 {
		state, deadline = "terminal", now
	}
	_, err = s.db.ExecContext(ctx, `UPDATE captures SET audio_attempts=audio_attempts+1,audio_state=?,state=CASE WHEN ?='terminal' THEN 'rejected' ELSE state END,cleanup_at=?,updated_at=? WHERE id=? AND household_id=? AND owner_user_id=?`, state, state, deadline.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id, actor.HouseholdID, actor.ActorID)
	return err
}

func (s *Service) CancelAudio(ctx context.Context, actor policy.ActorScope, id string) error {
	c, err := s.load(ctx, actor, id)
	if err != nil || c.raw == "" {
		return ErrNotFound
	}
	_, err = s.db.ExecContext(ctx, `UPDATE captures SET state='cancelled',audio_state='cancelled',cleanup_at=?,updated_at=? WHERE id=? AND household_id=? AND owner_user_id=?`, s.now().UTC().Format(time.RFC3339Nano), s.now().UTC().Format(time.RFC3339Nano), id, actor.HouseholdID, actor.ActorID)
	return err
}

// Discard removes an unconfirmed capture and its sources. When a typed record
// was already staged, the normal revision-fenced Undo path must succeed first.
func (s *Service) Discard(ctx context.Context, actor policy.ActorScope, id string) error {
	c, err := s.load(ctx, actor, id)
	if err != nil || (c.state != "processing" && c.state != "clarification" && c.state != "awaiting_confirmation") {
		return ErrNotFound
	}
	if c.recordID != "" {
		if err := s.Undo(ctx, actor, id); err != nil {
			return err
		}
	}
	for _, sourceID := range []string{c.raw, c.source} {
		if sourceID == "" {
			continue
		}
		if err := s.sources.Delete(ctx, actor, sourceID); err != nil && !errors.Is(err, storage.ErrNotFound) {
			return err
		}
	}
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `UPDATE captures SET state='cancelled',source_id=NULL,audio_state=CASE WHEN raw_audio_source_id IS NULL THEN 'none' ELSE 'cleaned' END,raw_audio_source_id=NULL,cleanup_at=NULL,updated_at=? WHERE id=? AND household_id=? AND owner_user_id=? AND state IN ('processing','clarification','undone')`, stamp, id, actor.HouseholdID, actor.ActorID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrNotFound
	}
	return nil
}

// Cleanup is safe to call at worker startup and after retry/abandonment deadlines.
func (s *Service) Cleanup(ctx context.Context, before time.Time) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id,household_id,owner_user_id,raw_audio_source_id FROM captures WHERE raw_audio_source_id IS NOT NULL AND cleanup_at IS NOT NULL AND cleanup_at<=?`, before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	type expired struct{ id, household, owner, raw string }
	var expiredRows []expired
	for rows.Next() {
		var item expired
		if err := rows.Scan(&item.id, &item.household, &item.owner, &item.raw); err != nil {
			rows.Close()
			return err
		}
		expiredRows = append(expiredRows, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range expiredRows {
		if err := s.sources.DeleteExpiredVoice(ctx, item.household, item.owner, item.raw); err != nil && !errors.Is(err, storage.ErrNotFound) {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE captures SET raw_audio_source_id=NULL,audio_state='cleaned',cleanup_at=NULL,updated_at=? WHERE id=?`, s.now().UTC().Format(time.RFC3339Nano), item.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Undo(ctx context.Context, actor policy.ActorScope, id string) error {
	c, err := s.load(ctx, actor, id)
	if err != nil || c.recordID == "" || (c.state != "awaiting_confirmation" && c.state != "confirmed") || (!c.undoUntil.IsZero() && s.now().After(c.undoUntil)) {
		return ErrUndoRefused
	}
	if c.visibility == policy.Shared {
		current, e := s.revision(ctx, actor, c.visibility)
		if e != nil || current != c.undoRevision {
			return ErrUndoRefused
		}
	}
	if !validTable(c.table, c.family) {
		return ErrUndoRefused
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE `+c.table+` SET active=0,version=version+1,updated_at=? WHERE id=? AND household_id=? AND active=1 AND version=?`, s.now().UTC().Format(time.RFC3339Nano), c.recordID, actor.HouseholdID, c.version)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrUndoRefused
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM search_entries WHERE record_family=? AND record_id=?`, c.family, c.recordID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE captures SET state='undone',updated_at=? WHERE id=?`, s.now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	return tx.Commit()
}

type row struct {
	raw, source, summary, state, audio, recordID, table, family, field, question, proposal, kind string
	attempts, version, undoRevision                                                              int64
	visibility                                                                                   policy.Visibility
	undoUntil                                                                                    time.Time
}

func (s *Service) load(ctx context.Context, actor policy.ActorScope, id string) (row, error) {
	var r row
	var v, until string
	err := s.db.QueryRowContext(ctx, `SELECT visibility,COALESCE(raw_audio_source_id,''),COALESCE(source_id,''),summary,source_kind,state,audio_state,audio_attempts,record_family,record_table,record_id,record_version,undo_revision,COALESCE(undo_until,''),clarification_field,clarification_question,proposal_json FROM captures WHERE id=? AND household_id=? AND owner_user_id=?`, id, actor.HouseholdID, actor.ActorID).Scan(&v, &r.raw, &r.source, &r.summary, &r.kind, &r.state, &r.audio, &r.attempts, &r.family, &r.table, &r.recordID, &r.version, &r.undoRevision, &until, &r.field, &r.question, &r.proposal)
	if err != nil {
		return r, ErrNotFound
	}
	r.visibility = policy.Visibility(v)
	if until != "" {
		r.undoUntil, _ = time.Parse(time.RFC3339Nano, until)
	}
	return r, nil
}
func (r row) capture(id string) Capture {
	return Capture{ID: id, SourceID: r.source, RawAudioSourceID: r.raw, Summary: r.summary, State: r.state, ClarificationField: r.field, ClarificationQuestion: r.question, RecordFamily: r.family, RecordID: r.recordID, AudioState: r.audio, Visibility: r.visibility, UndoUntil: r.undoUntil}
}
func (s *Service) revision(ctx context.Context, a policy.ActorScope, v policy.Visibility) (int64, error) {
	var r int64
	q := `SELECT personal_revision FROM user_revisions WHERE user_id=?`
	k := a.ActorID
	if v == policy.Shared {
		q = `SELECT shared_revision FROM household_revisions WHERE household_id=?`
		k = a.HouseholdID
	}
	return r, s.db.QueryRowContext(ctx, q, k).Scan(&r)
}
func (s *Service) create(ctx context.Context, a policy.ActorScope, r TextRequest, source storage.Source) (family, table, id string, version int64, err error) {
	p := r.Proposal
	if p.Finance != nil {
		d := p.Finance
		out, e := s.finance.Create(ctx, a, finance.Draft{Kind: d.Kind, Visibility: r.Visibility, Label: d.Label, Category: d.Category, Date: d.Date, EndDate: d.EndDate, Status: d.Status, AmountText: d.AmountText, IncompleteNote: d.IncompleteNote, CurrencyContext: d.CurrencyContext, Provenance: finance.Provenance{SourceID: source.ID, SourceFamily: source.Family, SourceVersion: source.Version, LocatorKind: "source", LocatorValue: source.ID, GeneratedBy: "application", SchemaVersion: "capture-v1"}})
		return "finance", financeTable(d.Kind), out.ID, out.Version, e
	}
	if p.Health != nil {
		d := p.Health
		prov := health.Provenance{SourceID: source.ID, SourceFamily: source.Family, SourceVersion: source.Version, LocatorKind: "source", LocatorValue: source.ID, GeneratedBy: "application", SchemaVersion: "capture-v1"}
		switch d.Kind {
		case Observation:
			out, e := s.health.CreateObservation(ctx, a, health.ObservationDraft{Visibility: r.Visibility, Subject: d.Subject, Analyte: d.Analyte, ObservedOn: d.ObservedOn, Value: d.Value, Unit: d.Unit, Provenance: prov})
			return "health", "health_observations", out.ID, out.Version, e
		case Appointment:
			out, e := s.health.CreateAppointment(ctx, a, health.AppointmentDraft{Visibility: r.Visibility, Subject: d.Subject, Label: d.Label, Provider: d.Provider, Location: d.Location, ScheduledOn: d.ScheduledOn, Status: d.Status, Provenance: prov})
			if e != nil {
				return "health", "health_appointments", "", 0, e
			}
			version, versionErr := recordVersion(ctx, s.db, "health_appointments", out.ID)
			return "health", "health_appointments", out.ID, version, versionErr
		case Routine:
			out, e := s.health.CreateRoutine(ctx, a, health.RoutineDraft{Visibility: r.Visibility, Subject: d.Subject, Label: d.Label, Cadence: d.Cadence, NextDueOn: d.NextDueOn, Status: d.Status, Provenance: prov})
			if e != nil {
				return "health", "health_care_routines", "", 0, e
			}
			version, versionErr := recordVersion(ctx, s.db, "health_care_routines", out.ID)
			return "health", "health_care_routines", out.ID, version, versionErr
		}
	}
	d := p.Planning
	out, e := s.planning.CreateEvent(ctx, a, planning.EventDraft{Visibility: r.Visibility, Title: d.Title, Description: d.Description, Location: d.Location, AllDay: d.AllDay, StartsOn: d.StartsOn, EndsOn: d.EndsOn, StartsAt: d.StartsAt, EndsAt: d.EndsAt, Timezone: d.Timezone, Status: d.Status, OwnerIDs: []string{a.ActorID}, Provenance: planning.Provenance{SourceID: source.ID, SourceFamily: source.Family, SourceVersion: source.Version, LocatorKind: "source", LocatorValue: source.ID, GeneratedBy: "application", SchemaVersion: "capture-v1"}})
	return "planning", "planning_events", out.ID, out.Version, e
}
func recordVersion(ctx context.Context, db *sql.DB, table, id string) (int64, error) {
	var v int64
	err := db.QueryRowContext(ctx, `SELECT version FROM `+table+` WHERE id=?`, id).Scan(&v)
	return v, err
}
func validRequest(a policy.ActorScope, r TextRequest) error {
	if !a.Valid() || !visibility(r.Visibility) || strings.TrimSpace(r.Text) == "" || len(r.Text) > 16<<20 || !safe(r.Summary, 512) || !validProposal(r.Proposal) {
		return ErrInvalidProposal
	}
	return nil
}
func validProposal(p Proposal) bool {
	n := 0
	if p.Finance != nil {
		n++
	}
	if p.Health != nil {
		n++
	}
	if p.Planning != nil {
		n++
	}
	if n != 1 {
		return false
	}
	switch p.Variant {
	case FinanceVariant:
		return p.Finance != nil && validFinance(*p.Finance)
	case HealthVariant:
		return p.Health != nil && validHealth(*p.Health)
	case PlanningVariant:
		return p.Planning != nil && validPlanning(*p.Planning)
	}
	return false
}
func validFinance(d FinanceProposal) bool {
	return financeTable(d.Kind) != "" && safe(d.Label, 256) && safe(d.Category, 128) && safe(d.Date, 10) && safe(d.EndDate, 10) && safe(d.Status, 16) && safe(d.AmountText, 128) && safe(d.IncompleteNote, 256) && safe(d.CurrencyContext, 16)
}
func validHealth(d HealthProposal) bool {
	return (d.Kind == Observation || d.Kind == Appointment || d.Kind == Routine) && safe(d.Subject, 256) && safe(d.Label, 256) && safe(d.Analyte, 256) && safe(d.ObservedOn, 10) && safe(d.Value, 128) && safe(d.Unit, 64) && safe(d.Provider, 256) && safe(d.Location, 512) && safe(d.ScheduledOn, 10) && safe(d.Cadence, 256) && safe(d.NextDueOn, 10) && safe(d.Status, 16)
}
func validPlanning(d PlanningProposal) bool {
	return safe(d.Title, 256) && safe(d.Description, 4000) && safe(d.Location, 512) && safe(d.StartsOn, 10) && safe(d.EndsOn, 10) && safe(d.StartsAt, 16) && safe(d.EndsAt, 16) && safe(d.Timezone, 64) && safe(d.Status, 16)
}
func clarification(p Proposal) (string, string) {
	if p.Finance != nil && strings.TrimSpace(p.Finance.Date) == "" {
		return "date", "What date should this finance record use?"
	}
	if p.Finance != nil && p.Finance.Kind == finance.Obligation && strings.TrimSpace(p.Finance.Status) == "" {
		return "status", "Is this obligation pending, paid, or cancelled?"
	}
	if p.Health != nil {
		d := p.Health
		if strings.TrimSpace(d.Subject) == "" {
			return "owner", "Who is this health record for?"
		}
		if (d.Kind == Observation && d.ObservedOn == "") || (d.Kind == Appointment && d.ScheduledOn == "") || (d.Kind == Routine && d.NextDueOn == "") {
			return "date", "What date should this health record use?"
		}
		if d.Kind == Observation && d.Unit == "" {
			return "unit", "What unit was recorded?"
		}
		if d.Kind != Observation && d.Status == "" {
			return "status", "What is this health record's status?"
		}
	}
	if p.Planning != nil {
		d := p.Planning
		if d.AllDay && d.StartsOn == "" {
			return "date", "What date is this planned for? Use YYYY-MM-DD."
		}
		if !d.AllDay && d.StartsAt == "" {
			return "date", "When does this start? Use YYYY-MM-DDTHH:MM."
		}
		if !d.AllDay && d.EndsAt == "" {
			return "date", "When does this end? Use YYYY-MM-DDTHH:MM."
		}
		if d.Status == "" {
			return "status", "What is this plan's status?"
		}
	}
	return "", ""
}
func summary(r TextRequest) string {
	if strings.TrimSpace(r.Summary) != "" {
		return strings.TrimSpace(r.Summary)
	}
	v := strings.TrimSpace(r.Text)
	if len(v) > 512 {
		v = v[:512]
	}
	return v
}
func safe(v string, max int) bool         { return len(v) <= max && !strings.ContainsAny(v, "\x00<>") }
func visibility(v policy.Visibility) bool { return v == policy.Personal || v == policy.Shared }
func audioState(raw string) string {
	if raw != "" {
		return "retryable"
	}
	return "none"
}
func captureID() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	return hex.EncodeToString(b[:]), nil
}
func financeTable(k finance.Kind) string {
	switch k {
	case finance.Income:
		return "finance_income"
	case finance.Spending:
		return "finance_spending"
	case finance.Asset:
		return "finance_assets"
	case finance.Liability:
		return "finance_liabilities"
	case finance.Budget:
		return "finance_budgets"
	case finance.Obligation:
		return "finance_obligations"
	}
	return ""
}
func validTable(table, family string) bool {
	return (family == "finance" && strings.HasPrefix(table, "finance_")) || (family == "health" && (table == "health_observations" || table == "health_appointments" || table == "health_care_routines")) || (family == "planning" && table == "planning_events")
}
