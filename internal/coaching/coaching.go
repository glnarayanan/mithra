// Package coaching builds privacy-fenced, evidence-linked household overviews.
package coaching

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/glnarayanan/mithra/internal/health"
	"github.com/glnarayanan/mithra/internal/policy"
)

const (
	PromptVersion = "coaching-v6"
	SchemaVersion = "coaching-v3"
)

var (
	ErrInvalid      = errors.New("coaching input is invalid")
	ErrStale        = errors.New("coaching context changed")
	ErrUnsupported  = errors.New("coaching output is unsupported by evidence")
	ErrNudgeMissing = errors.New("nudge is unavailable")
)

type Fact struct {
	EvidenceID   string            `json:"evidence_id"`
	Family       string            `json:"family"`
	Kind         string            `json:"-"`
	RecordID     string            `json:"-"`
	Content      string            `json:"content"`
	Date         string            `json:"date,omitempty"`
	Time         string            `json:"time,omitempty"`
	Issue        string            `json:"issue,omitempty"`
	SourceID     string            `json:"-"`
	Visibility   policy.Visibility `json:"visibility"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"-"`
	Status       string            `json:"-"`
	SupersedesID string            `json:"-"`
}

type Context struct {
	HouseholdID       string   `json:"household_id"`
	Scope             string   `json:"scope"`
	SharedRevision    int64    `json:"shared_revision"`
	PersonalRevision  int64    `json:"personal_revision"`
	SourceFingerprint string   `json:"source_fingerprint"`
	Facts             []Fact   `json:"facts"`
	ReviewFacts       []Fact   `json:"-"`
	Signals           []Signal `json:"signals"`
}

// Signal is a deterministic observation prepared from visible typed records.
// It gives the model facts to explain, rather than asking it to do arithmetic.
type Signal struct {
	Kind        string   `json:"kind"`
	Summary     string   `json:"summary"`
	Period      string   `json:"period"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type amountRow struct {
	coefficient, scale int64
	date, category     string
	evidence           string
	source             string
}

type evidenceRef struct{ id, source string }

type Item struct {
	Title       string   `json:"title"`
	Copy        string   `json:"copy"`
	When        string   `json:"when,omitempty"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type Narrative struct {
	Lead            Item   `json:"lead"`
	Insights        []Item `json:"insights"`
	Changes         []Item `json:"changes"`
	Dates           []Item `json:"dates"`
	Inconsistencies []Item `json:"inconsistencies"`
	Priorities      []Item `json:"priorities"`
}

type Overview struct {
	Shared                         Narrative
	Personal                       Narrative
	SharedContext                  Context
	PersonalContext                Context
	HasRecords                     bool
	SharedCache, PersonalCache     CacheState
	SharedHistory, PersonalHistory []History
}

// ReviewEvent is one typed record in one week-review section. It deliberately
// does not reuse the Family Brief's Narrative sections.
type ReviewEvent struct {
	Fact                     Fact
	Facts                    []Fact
	Title, Copy, When, Time  string
	Domain, Visibility       string
	Status, Reason, NextStep string
	EvidenceID               string
	EvidenceIDs              []string
	Overdue                  bool
}

// ReviewStatus is a deterministic shared or personal weekly readout.
type ReviewStatus struct{ Label, Copy string }

type ReviewScope struct {
	Changes, Upcoming, Issues, Priorities, Progress []ReviewEvent
	Observation                                     Item
	Status                                          ReviewStatus
	Insights                                        []Item
	Context                                         Context
	Cache                                           CacheState
	History                                         []History
}

// WeeklyReview keeps the review's deterministic record classification separate
// from the Family Brief. AI wording remains available as an optional insight.
type WeeklyReview struct {
	Shared, Personal ReviewScope
	HasRecords       bool
}

type CacheState struct {
	Found, Stale bool
	GeneratedAt  time.Time
	Model        string
}

// History is a retained, actor-scoped copy of a successful coaching response.
type History struct {
	Narrative   Narrative
	Model       string
	GeneratedAt time.Time
}

type Nudge struct {
	ID, Family, RecordID, SourceID, State                string
	FollowUpEnabled, InitialEmailSent, FollowUpEmailSent bool
	CreatedAt                                            time.Time
}

type Service struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB) *Service { return &Service{db: db, now: time.Now} }

// BuildContext is the only prompt-context constructor. Shared construction
// cannot read a personal row; personal construction is owner-only.
func (s *Service) BuildContext(ctx context.Context, actor policy.ActorScope, visibility policy.Visibility) (Context, error) {
	if s == nil || s.db == nil || !actor.Valid() || (visibility != policy.Shared && visibility != policy.Personal) {
		return Context{}, policy.ErrUnauthorized
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Context{}, err
	}
	defer tx.Rollback()
	result, err := s.buildContextTx(ctx, tx, actor, visibility)
	if err != nil {
		return Context{}, err
	}
	if err := tx.Commit(); err != nil {
		return Context{}, err
	}
	return result, nil
}

func (s *Service) buildContextTx(ctx context.Context, tx *sql.Tx, actor policy.ActorScope, visibility policy.Visibility) (Context, error) {
	shared, personal, err := revisions(ctx, tx, actor)
	if err != nil {
		return Context{}, policy.ErrUnauthorized
	}
	facts, err := queryFacts(ctx, tx, actor, visibility)
	if err != nil {
		return Context{}, err
	}
	filter := health.SharedRecords
	if visibility == policy.Personal {
		filter = health.PersonalRecords
	}
	healthSummary, err := health.New(s.db).SummarizeInTx(ctx, tx, actor, filter)
	if err != nil {
		return Context{}, err
	}
	applyHealthConflicts(facts, healthSummary.Conflicts)
	signals, err := querySignals(ctx, tx, actor, visibility, facts, healthSummary, s.now().UTC())
	if err != nil {
		return Context{}, err
	}
	fingerprint := sourceFingerprint(facts)
	personalKey := personal
	if visibility == policy.Shared {
		personalKey = 0
	}
	return Context{HouseholdID: actor.HouseholdID, Scope: string(visibility), SharedRevision: shared, PersonalRevision: personalKey, SourceFingerprint: fingerprint, Facts: coachingFacts(facts), ReviewFacts: facts, Signals: signals}, nil
}

func (s *Service) Overview(ctx context.Context, actor policy.ActorScope, asOf time.Time) (Overview, error) {
	shared, err := s.BuildContext(ctx, actor, policy.Shared)
	if err != nil {
		return Overview{}, err
	}
	personal, err := s.BuildContext(ctx, actor, policy.Personal)
	if err != nil {
		return Overview{}, err
	}
	result := Overview{Shared: deterministic(shared.ReviewFacts, asOf, shared.Signals...), Personal: deterministic(personal.ReviewFacts, asOf, personal.Signals...), SharedContext: shared, PersonalContext: personal, HasRecords: len(shared.ReviewFacts)+len(personal.ReviewFacts) > 0}
	if cached, state, err := s.load(ctx, actor, "brief", policy.Shared, shared, asOf); err == nil && state.Found {
		if state.Stale {
			cached.Dates = result.Shared.Dates
			cached.Inconsistencies = result.Shared.Inconsistencies
			cached.Priorities = result.Shared.Priorities
		}
		cached = withSignals(cached, shared.Signals)
		result.Shared, result.SharedCache = cached, state
	} else {
		result.SharedCache = state
	}
	if cached, state, err := s.load(ctx, actor, "brief", policy.Personal, personal, asOf); err == nil && state.Found {
		if state.Stale {
			cached.Dates = result.Personal.Dates
			cached.Inconsistencies = result.Personal.Inconsistencies
			cached.Priorities = result.Personal.Priorities
		}
		cached = withSignals(cached, personal.Signals)
		result.Personal, result.PersonalCache = cached, state
	} else {
		result.PersonalCache = state
	}
	result.SharedHistory, _ = s.history(ctx, actor, "brief", policy.Shared, shared)
	result.PersonalHistory, _ = s.history(ctx, actor, "brief", policy.Personal, personal)
	return result, nil
}

func (s *Service) Week(ctx context.Context, actor policy.ActorScope, asOf time.Time) (WeeklyReview, error) {
	shared, err := s.BuildContext(ctx, actor, policy.Shared)
	if err != nil {
		return WeeklyReview{}, err
	}
	personal, err := s.BuildContext(ctx, actor, policy.Personal)
	if err != nil {
		return WeeklyReview{}, err
	}
	result := WeeklyReview{Shared: weeklyScope(shared, asOf), Personal: weeklyScope(personal, asOf), HasRecords: len(shared.ReviewFacts)+len(personal.ReviewFacts) > 0}
	if cached, state, err := s.load(ctx, actor, "week", policy.Shared, shared, asOf); err == nil && state.Found {
		result.Shared.Insights = cachedReviewInsights(withSignals(cached, shared.Signals).Insights)
		result.Shared.Cache = state
	} else {
		result.Shared.Cache = state
	}
	if cached, state, err := s.load(ctx, actor, "week", policy.Personal, personal, asOf); err == nil && state.Found {
		result.Personal.Insights = reviewInsights(withSignals(cached, personal.Signals).Insights, result.Personal)
		result.Personal.Cache = state
	} else {
		result.Personal.Cache = state
	}
	result.Shared.History, _ = s.history(ctx, actor, "week", policy.Shared, shared)
	result.Personal.History, _ = s.history(ctx, actor, "week", policy.Personal, personal)
	return result, nil
}

// Publish rebuilds permitted context immediately before storing model wording.
// Shared and personal results are validated and cached independently.
func (s *Service) Publish(ctx context.Context, actor policy.ActorScope, mode string, visibility policy.Visibility, expected Context, output Narrative, model string) error {
	if s == nil || s.db == nil || !actor.Valid() || (visibility != policy.Shared && visibility != policy.Personal) {
		return policy.ErrUnauthorized
	}
	if mode != "brief" && mode != "week" || strings.TrimSpace(model) == "" || len(model) > 64 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := s.buildContextTx(ctx, tx, actor, visibility)
	if err != nil {
		return err
	}
	if !sameContext(current, expected) {
		return ErrStale
	}
	allowed := make(map[string]Fact, len(current.Facts))
	for _, fact := range current.Facts {
		allowed[fact.EvidenceID] = fact
	}
	fallback := deterministic(current.Facts, s.now(), current.Signals...).Lead
	output, err = sanitizeNarrative(output, allowed, fallback, current.Signals...)
	if err != nil {
		return err
	}
	content, _ := json.Marshal(output)
	evidence, _ := json.Marshal(current.Facts)
	if len(content) > 256<<10 || len(evidence) > 128<<10 {
		return ErrInvalid
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	owner := any(nil)
	if visibility == policy.Personal {
		owner = actor.ActorID
	}
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO coaching_cache(id,household_id,owner_user_id,mode,visibility,content_json,evidence_json,shared_revision,personal_revision,source_fingerprint,model,prompt_version,schema_version,generated_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(household_id,IFNULL(owner_user_id,''),mode,visibility) DO UPDATE SET content_json=excluded.content_json,evidence_json=excluded.evidence_json,shared_revision=excluded.shared_revision,personal_revision=excluded.personal_revision,source_fingerprint=excluded.source_fingerprint,model=excluded.model,prompt_version=excluded.prompt_version,schema_version=excluded.schema_version,generated_at=excluded.generated_at,updated_at=excluded.updated_at`, id, actor.HouseholdID, owner, mode, visibility, string(content), string(evidence), current.SharedRevision, current.PersonalRevision, current.SourceFingerprint, model, PromptVersion, SchemaVersion, stamp, stamp, stamp)
	if err != nil {
		return err
	}
	historyID, err := randomID()
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO coaching_history(id,household_id,owner_user_id,mode,visibility,content_json,evidence_json,model,prompt_version,schema_version,generated_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, historyID, actor.HouseholdID, owner, mode, visibility, string(content), string(evidence), model, PromptVersion, SchemaVersion, stamp, stamp); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM coaching_history WHERE id IN (SELECT id FROM coaching_history WHERE household_id=? AND IFNULL(owner_user_id,'')=IFNULL(?, '') AND mode=? AND visibility=? ORDER BY generated_at DESC,id DESC LIMIT -1 OFFSET 12)`, actor.HouseholdID, owner, mode, visibility); err != nil {
		return err
	}
	return tx.Commit()
}

// History returns only the requesting adult's private snapshots or household
// shared snapshots. It never falls back across visibility.
func (s *Service) History(ctx context.Context, actor policy.ActorScope, mode string, visibility policy.Visibility) ([]History, error) {
	if s == nil || s.db == nil || !actor.Valid() || (mode != "brief" && mode != "week") || (visibility != policy.Shared && visibility != policy.Personal) {
		return nil, policy.ErrUnauthorized
	}
	current, err := s.BuildContext(ctx, actor, visibility)
	if err != nil {
		return nil, err
	}
	return s.history(ctx, actor, mode, visibility, current)
}

func (s *Service) history(ctx context.Context, actor policy.ActorScope, mode string, visibility policy.Visibility, current Context) ([]History, error) {
	owner := ""
	if visibility == policy.Personal {
		owner = actor.ActorID
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,content_json,evidence_json,model,generated_at FROM coaching_history WHERE household_id=? AND IFNULL(owner_user_id,'')=? AND mode=? AND visibility=? ORDER BY generated_at DESC,id DESC LIMIT 12`, actor.HouseholdID, owner, mode, visibility)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []History{}
	stale := []string{}
	for rows.Next() {
		var id, encoded, evidence, generated string
		var history History
		if err := rows.Scan(&id, &encoded, &evidence, &history.Model, &generated); err != nil {
			return nil, err
		}
		var facts []Fact
		if json.Unmarshal([]byte(encoded), &history.Narrative) != nil || json.Unmarshal([]byte(evidence), &facts) != nil || !evidenceStillVisible(facts, current.Facts) {
			stale = append(stale, id)
			continue
		}
		history.GeneratedAt, _ = time.Parse(time.RFC3339Nano, generated)
		out = append(out, history)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, id := range stale {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM coaching_history WHERE id=? AND household_id=?`, id, actor.HouseholdID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Service) load(ctx context.Context, actor policy.ActorScope, mode string, visibility policy.Visibility, current Context, asOf time.Time) (Narrative, CacheState, error) {
	owner := actor.ActorID
	if visibility == policy.Shared {
		owner = ""
	}
	var encoded, evidence, generated, promptVersion, schemaVersion, model string
	var shared, personal int64
	err := s.db.QueryRowContext(ctx, `SELECT content_json,evidence_json,shared_revision,personal_revision,generated_at,prompt_version,schema_version,model FROM coaching_cache WHERE household_id=? AND IFNULL(owner_user_id,'')=? AND mode=? AND visibility=?`, actor.HouseholdID, owner, mode, visibility).Scan(&encoded, &evidence, &shared, &personal, &generated, &promptVersion, &schemaVersion, &model)
	if err != nil {
		return Narrative{}, CacheState{}, err
	}
	if promptVersion != PromptVersion || schemaVersion != SchemaVersion {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM coaching_cache WHERE household_id=? AND IFNULL(owner_user_id,'')=? AND mode=? AND visibility=?`, actor.HouseholdID, owner, mode, visibility)
		return Narrative{}, CacheState{}, ErrStale
	}
	state := CacheState{Found: true, Model: model}
	state.GeneratedAt, _ = time.Parse(time.RFC3339Nano, generated)
	var facts []Fact
	if json.Unmarshal([]byte(evidence), &facts) != nil || !evidenceStillVisible(facts, current.Facts) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM coaching_cache WHERE household_id=? AND IFNULL(owner_user_id,'')=? AND mode=? AND visibility=?`, actor.HouseholdID, owner, mode, visibility)
		return Narrative{}, CacheState{}, ErrStale
	}
	state.Stale = shared != current.SharedRevision || personal != current.PersonalRevision || !day(state.GeneratedAt).Equal(day(asOf.UTC()))
	var out Narrative
	if json.Unmarshal([]byte(encoded), &out) != nil {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM coaching_cache WHERE household_id=? AND IFNULL(owner_user_id,'')=? AND mode=? AND visibility=?`, actor.HouseholdID, owner, mode, visibility)
		return Narrative{}, CacheState{}, ErrInvalid
	}
	return out, state, nil
}

func queryFacts(ctx context.Context, tx *sql.Tx, actor policy.ActorScope, visibility policy.Visibility) ([]Fact, error) {
	ownerClause := ""
	args := []any{actor.HouseholdID, string(visibility)}
	if visibility == policy.Personal {
		ownerClause = " AND r.owner_user_id=?"
		args = append(args, actor.ActorID)
	}
	rows, err := tx.QueryContext(ctx, `WITH records AS (
	SELECT 'finance' family,'income' kind,id,household_id,owner_user_id,visibility,source_id,label title,received_on date,'' time,'' status,incomplete_reason issue,created_at,updated_at,COALESCE(supersedes_id,'') supersedes_id FROM finance_income WHERE active=1
	UNION ALL SELECT 'finance','spending',id,household_id,owner_user_id,visibility,source_id,label,spent_on,'','',incomplete_reason,created_at,updated_at,COALESCE(supersedes_id,'') FROM finance_spending WHERE active=1
	UNION ALL SELECT 'finance','asset',id,household_id,owner_user_id,visibility,source_id,label,observed_on,'','',incomplete_reason,created_at,updated_at,COALESCE(supersedes_id,'') FROM finance_assets WHERE active=1
	UNION ALL SELECT 'finance','liability',id,household_id,owner_user_id,visibility,source_id,label,observed_on,'','',incomplete_reason,created_at,updated_at,COALESCE(supersedes_id,'') FROM finance_liabilities WHERE active=1
	UNION ALL SELECT 'finance','budget',id,household_id,owner_user_id,visibility,source_id,label,starts_on,'','',incomplete_reason,created_at,updated_at,COALESCE(supersedes_id,'') FROM finance_budgets WHERE active=1
	UNION ALL SELECT 'finance','obligation',id,household_id,owner_user_id,visibility,source_id,label,due_on,'',status,incomplete_reason,created_at,updated_at,COALESCE(supersedes_id,'') FROM finance_obligations WHERE active=1
	UNION ALL SELECT 'health','observation',id,household_id,owner_user_id,visibility,source_id,TRIM(analyte || CASE WHEN subject<>'' THEN ' for ' || subject ELSE '' END),observed_on,'','','',created_at,updated_at,COALESCE(supersedes_id,'') FROM health_observations WHERE active=1
	UNION ALL SELECT 'health','appointment',id,household_id,owner_user_id,visibility,source_id,label,scheduled_on,'',status,'',created_at,updated_at,'' FROM health_appointments WHERE active=1
	UNION ALL SELECT 'health','routine',id,household_id,owner_user_id,visibility,source_id,label,next_due_on,'',status,'',created_at,updated_at,'' FROM health_care_routines WHERE active=1
	UNION ALL SELECT 'planning','goal',id,household_id,owner_user_id,visibility,source_id,title,target_on,'',status,'',created_at,updated_at,'' FROM planning_goals WHERE active=1
	UNION ALL SELECT 'planning','plan',id,household_id,owner_user_id,visibility,source_id,title,'','',status,'',created_at,updated_at,'' FROM planning_plans WHERE active=1
	UNION ALL SELECT 'planning','milestone',id,household_id,owner_user_id,visibility,source_id,title,due_on,'',status,'',created_at,updated_at,'' FROM planning_milestones WHERE active=1
	UNION ALL SELECT 'planning','event',id,household_id,owner_user_id,visibility,source_id,title,COALESCE(NULLIF(starts_on,''),substr(starts_at,1,10)),CASE WHEN all_day=1 THEN '' ELSE substr(starts_at,12,5) END,status,'',created_at,updated_at,'' FROM planning_events WHERE active=1
	)
	SELECT DISTINCT r.family,r.kind,r.id,r.title,r.date,r.time,r.status,r.issue,r.visibility,el.source_id,r.created_at,r.updated_at,r.supersedes_id
	FROM records r
	JOIN evidence_links el ON el.record_family=r.family AND el.record_id=r.id AND el.household_id=r.household_id AND el.visibility=r.visibility AND el.owner_user_id=r.owner_user_id AND el.source_id=r.source_id
	JOIN sources src ON src.id=el.source_id AND src.state='live' AND src.household_id=r.household_id
	 AND ((r.visibility='shared' AND src.visibility='shared') OR (r.visibility='personal' AND (src.visibility='shared' OR (src.visibility='personal' AND src.owner_user_id=r.owner_user_id))))
	WHERE r.household_id=? AND r.visibility=?`+ownerClause+`
	ORDER BY r.created_at,r.family,r.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Fact
	for rows.Next() {
		var f Fact
		var created, updated, visible string
		if err := rows.Scan(&f.Family, &f.Kind, &f.RecordID, &f.Content, &f.Date, &f.Time, &f.Status, &f.Issue, &visible, &f.SourceID, &created, &updated, &f.SupersedesID); err != nil {
			return nil, err
		}
		f.Visibility = policy.Visibility(visible)
		f.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		f.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		f.EvidenceID = evidenceID(actor.HouseholdID, f)
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := markConflicts(ctx, tx, actor.HouseholdID, out); err != nil {
		return nil, err
	}
	return out, nil
}

// querySignals builds bounded summaries only from records already present in
// the actor-scoped fact set. The model gets these facts, not a request to infer
// calculations from prose.
func querySignals(ctx context.Context, tx *sql.Tx, actor policy.ActorScope, visibility policy.Visibility, facts []Fact, healthSummary health.Summary, asOf time.Time) ([]Signal, error) {
	byRecord := make(map[string]Fact, len(facts))
	for _, fact := range facts {
		if fact.Issue == "" {
			byRecord[fact.RecordID] = fact
		}
	}
	ownerClause, args := "", []any{actor.HouseholdID, string(visibility)}
	if visibility == policy.Personal {
		ownerClause, args = " AND owner_user_id=?", append(args, actor.ActorID)
	}
	var out []Signal

	// Finance: compare equal portions of the current and prior month. A partial
	// current month must never be compared with a complete prior month.
	rows, err := tx.QueryContext(ctx, `SELECT id,spent_on,category,amount_coefficient,amount_scale FROM finance_spending WHERE household_id=? AND visibility=? AND active=1`+ownerClause+` AND amount_coefficient IS NOT NULL ORDER BY spent_on DESC,id`, args...)
	if err != nil {
		return nil, err
	}
	months := map[string][]amountRow{}
	for rows.Next() {
		var id, date, category string
		var coefficient, scale int64
		if err := rows.Scan(&id, &date, &category, &coefficient, &scale); err != nil {
			rows.Close()
			return nil, err
		}
		parsedDate, err := time.Parse("2006-01-02", date)
		if err != nil {
			continue
		}
		if fact, ok := byRecord[id]; ok {
			month := parsedDate.Format("2006-01")
			months[month] = append(months[month], amountRow{coefficient, scale, date, category, fact.EvidenceID, fact.SourceID})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	latestStart := time.Date(asOf.Year(), asOf.Month(), 1, 0, 0, 0, 0, time.UTC)
	priorStart := latestStart.AddDate(0, -1, 0)
	latestMonth, priorMonth := latestStart.Format("2006-01"), priorStart.Format("2006-01")
	cutoff := asOf.Day()
	if priorDays := priorStart.AddDate(0, 1, -1).Day(); priorDays < cutoff {
		cutoff = priorDays
	}
	currentRows := rowsThroughDay(months[latestMonth], asOf.Day())
	latestRows := rowsThroughDay(months[latestMonth], cutoff)
	priorRows := rowsThroughDay(months[priorMonth], cutoff)
	if count := len(latestRows) + len(priorRows); count > 0 {
		evidence := amountEvidence(append(latestRows, priorRows...), 12)
		if len(evidence) > 0 {
			latestCutoff := latestStart.AddDate(0, 0, cutoff-1).Format("2006-01-02")
			priorCutoff := priorStart.AddDate(0, 0, cutoff-1).Format("2006-01-02")
			out = append(out, Signal{Kind: "finance_month_to_date", Period: displayDate(priorStart.Format("2006-01-02")) + " to " + displayDate(latestCutoff), Summary: "Spending recorded from " + displayDate(latestStart.Format("2006-01-02")) + " through " + displayDate(latestCutoff) + " is " + sumAmounts(latestRows) + ", compared with " + sumAmounts(priorRows) + " from " + displayDate(priorStart.Format("2006-01-02")) + " through " + displayDate(priorCutoff) + ".", EvidenceIDs: evidence})
		}
	}

	// Compare current month-to-date spending with one active budget that covers
	// today. Multiple overlapping budgets are not combined because they may
	// describe different categories.
	var budgetID, budgetLabel, budgetCategory string
	var budgetCoefficient, budgetScale int64
	err = tx.QueryRowContext(ctx, `SELECT id,label,category,amount_coefficient,amount_scale FROM finance_budgets WHERE household_id=? AND visibility=? AND active=1`+ownerClause+` AND amount_coefficient IS NOT NULL AND starts_on<=? AND ends_on>=? ORDER BY starts_on,id LIMIT 1`, append(args, day(asOf).Format("2006-01-02"), day(asOf).Format("2006-01-02"))...).Scan(&budgetID, &budgetLabel, &budgetCategory, &budgetCoefficient, &budgetScale)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		if fact, ok := byRecord[budgetID]; ok {
			budget := amountRow{budgetCoefficient, budgetScale, day(asOf).Format("2006-01-02"), budgetCategory, fact.EvidenceID, fact.SourceID}
			categoryRows := rowsInCategory(currentRows, budgetCategory)
			evidence := amountEvidence(append(append([]amountRow(nil), categoryRows...), budget), 12)
			spent, limit := scaledAmountTotal(categoryRows), scaledAmountTotal([]amountRow{budget})
			if len(evidence) > 0 && limit.Sign() > 0 {
				remaining := new(big.Int).Sub(new(big.Int).Set(limit), spent)
				out = append(out, Signal{Kind: "finance_budget", Period: latestStart.Format("Jan 2006"), Summary: "Spending recorded in " + budgetCategory + " through " + displayDate(day(asOf).Format("2006-01-02")) + " is " + formatScaledAmount(spent) + " against the " + formatScaledAmount(limit) + " budget recorded as " + budgetLabel + ", leaving " + formatScaledAmount(remaining) + ". This is " + formatPercent(spent, limit) + " of the recorded budget.", EvidenceIDs: evidence})
			}
		}
	}

	// Surface pending obligations dated in the next 31 days.
	start, end := day(asOf), day(asOf).AddDate(0, 0, 31)
	rows, err = tx.QueryContext(ctx, `SELECT id,due_on,amount_coefficient,amount_scale FROM finance_obligations WHERE household_id=? AND visibility=? AND active=1`+ownerClause+` AND status='pending' AND amount_coefficient IS NOT NULL AND due_on>=? AND due_on<? ORDER BY due_on,id`, append(args, start.Format("2006-01-02"), end.Format("2006-01-02"))...)
	if err != nil {
		return nil, err
	}
	var obligations []amountRow
	for rows.Next() {
		var id, date string
		var coefficient, scale int64
		if err := rows.Scan(&id, &date, &coefficient, &scale); err != nil {
			rows.Close()
			return nil, err
		}
		if fact, ok := byRecord[id]; ok {
			obligations = append(obligations, amountRow{coefficient, scale, date, "", fact.EvidenceID, fact.SourceID})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(obligations) > 0 {
		evidence := amountEvidence(obligations, 12)
		if len(evidence) > 0 {
			label, verb := "obligations", "are"
			if len(obligations) == 1 {
				label = "obligation"
				verb = "is"
			}
			out = append(out, Signal{Kind: "finance_obligations", Period: displayDate(start.Format("2006-01-02")) + " to " + displayDate(end.AddDate(0, 0, -1).Format("2006-01-02")), Summary: strconv.Itoa(len(obligations)) + " pending finance " + label + " totaling " + sumAmounts(obligations) + " " + verb + " dated in the next 31 days, from " + displayDate(obligations[0].date) + " to " + displayDate(obligations[len(obligations)-1].date) + ".", EvidenceIDs: evidence})
		}
	}

	// Health uses the Health service's unit and comparability rules. Conflicting
	// observations never reach this loop because they are absent from Series.
	healthSignals := 0
	for _, series := range healthSummary.Series {
		if len(series.Observations) < 2 {
			continue
		}
		first, last := series.Observations[0], series.Observations[len(series.Observations)-1]
		firstFact, firstOK := byRecord[first.ID]
		lastFact, lastOK := byRecord[last.ID]
		if !firstOK || !lastOK {
			continue
		}
		evidence := completeSourceEvidence([]evidenceRef{{firstFact.EvidenceID, firstFact.SourceID}, {lastFact.EvidenceID, lastFact.SourceID}}, 12)
		if len(evidence) == 0 {
			continue
		}
		period := displayDate(first.ObservedOn) + " to " + displayDate(last.ObservedOn)
		out = append(out, Signal{Kind: "health_series", Period: period, Summary: series.Analyte + " for " + series.Subject + " changed from " + first.Value.PlainString() + " " + series.Unit + " on " + displayDate(first.ObservedOn) + " to " + last.Value.PlainString() + " " + series.Unit + " on " + displayDate(last.ObservedOn) + ". This is a record comparison, not health advice.", EvidenceIDs: evidence})
		healthSignals++
		if healthSignals == 3 {
			break
		}
	}

	// Planning: dates are already selected from the typed records in queryFacts.
	upcoming := make([]Fact, 0, 6)
	start, end = day(asOf), day(asOf).AddDate(0, 0, 31)
	for _, fact := range facts {
		if fact.Family != "planning" || fact.Issue != "" {
			continue
		}
		if date, err := time.Parse("2006-01-02", fact.Date); err == nil && !date.Before(start) && date.Before(end) {
			upcoming = append(upcoming, fact)
		}
	}
	if len(upcoming) > 0 {
		sort.Slice(upcoming, func(i, j int) bool { return upcoming[i].Date < upcoming[j].Date })
		evidence := completeFactEvidence(upcoming, 12)
		if len(evidence) > 0 {
			out = append(out, Signal{Kind: "planning_upcoming", Period: displayDate(start.Format("2006-01-02")) + " to " + displayDate(end.AddDate(0, 0, -1).Format("2006-01-02")), Summary: strconv.Itoa(len(upcoming)) + " dated planning records fall in the next 31 days, from " + displayDate(upcoming[0].Date) + " to " + displayDate(upcoming[len(upcoming)-1].Date) + ".", EvidenceIDs: evidence})
		}
	}

	return out, nil
}

func rowsThroughDay(rows []amountRow, cutoff int) []amountRow {
	out := make([]amountRow, 0, len(rows))
	for _, row := range rows {
		date, err := time.Parse("2006-01-02", row.date)
		if err == nil && date.Day() <= cutoff {
			out = append(out, row)
		}
	}
	return out
}

func rowsInCategory(rows []amountRow, category string) []amountRow {
	out := make([]amountRow, 0, len(rows))
	for _, row := range rows {
		if strings.EqualFold(strings.TrimSpace(row.category), strings.TrimSpace(category)) {
			out = append(out, row)
		}
	}
	return out
}

func amountEvidence(rows []amountRow, limit int) []string {
	refs := make([]evidenceRef, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, evidenceRef{row.evidence, row.source})
	}
	return completeSourceEvidence(refs, limit)
}

func completeFactEvidence(facts []Fact, limit int) []string {
	refs := make([]evidenceRef, 0, len(facts))
	for _, fact := range facts {
		refs = append(refs, evidenceRef{fact.EvidenceID, fact.SourceID})
	}
	return completeSourceEvidence(refs, limit)
}

func completeSourceEvidence(refs []evidenceRef, limit int) []string {
	ids := make([]string, 0, limit)
	sources := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		source := ref.source
		if source == "" {
			source = ref.id
		}
		if _, seen := sources[source]; seen {
			continue
		}
		if len(ids) == limit {
			return nil
		}
		sources[source] = struct{}{}
		ids = appendEvidence(ids, ref.id, limit)
	}
	return ids
}

func appendEvidence(ids []string, id string, limit int) []string {
	for _, existing := range ids {
		if existing == id {
			return ids
		}
	}
	if len(ids) < limit {
		return append(ids, id)
	}
	return ids
}

func sumAmounts(rows []amountRow) string {
	return formatScaledAmount(scaledAmountTotal(rows))
}

func scaledAmountTotal(rows []amountRow) *big.Int {
	total := new(big.Int)
	for _, row := range rows {
		value := big.NewInt(row.coefficient)
		if row.scale < 6 {
			value.Mul(value, new(big.Int).Exp(big.NewInt(10), big.NewInt(6-row.scale), nil))
		}
		total.Add(total, value)
	}
	return total
}

func formatScaledAmount(total *big.Int) string {
	total = new(big.Int).Set(total)
	negative := total.Sign() < 0
	if negative {
		total.Abs(total)
	}
	raw := total.Text(10)
	for len(raw) < 7 {
		raw = "0" + raw
	}
	whole, fraction := raw[:len(raw)-6], strings.TrimRight(raw[len(raw)-6:], "0")
	whole = groupedNumber(whole)
	if fraction != "" {
		whole += "." + fraction
	}
	if negative {
		return "-" + whole
	}
	return whole
}

func groupedNumber(value string) string {
	if len(value) <= 3 {
		return value
	}
	first := len(value) % 3
	if first == 0 {
		first = 3
	}
	var out strings.Builder
	out.WriteString(value[:first])
	for index := first; index < len(value); index += 3 {
		out.WriteByte(',')
		out.WriteString(value[index : index+3])
	}
	return out.String()
}

func formatPercent(value, total *big.Int) string {
	if total.Sign() <= 0 {
		return "0%"
	}
	negative := value.Sign() < 0
	numerator := new(big.Int).Abs(new(big.Int).Set(value))
	numerator.Mul(numerator, big.NewInt(1_000))
	numerator.Add(numerator, new(big.Int).Quo(new(big.Int).Set(total), big.NewInt(2)))
	tenths := numerator.Quo(numerator, total)
	whole, fraction := new(big.Int).QuoRem(tenths, big.NewInt(10), new(big.Int))
	if fraction.Sign() == 0 {
		if negative {
			return "-" + whole.String() + "%"
		}
		return whole.String() + "%"
	}
	prefix := ""
	if negative {
		prefix = "-"
	}
	return prefix + whole.String() + "." + fmt.Sprintf("%d", fraction.Int64()) + "%"
}

func markConflicts(ctx context.Context, tx *sql.Tx, householdID string, facts []Fact) error {
	index := make(map[string]int, len(facts))
	for i, f := range facts {
		index[f.Family+"\x00"+f.RecordID] = i
	}
	mark := func(family, first, second, message string) {
		a, aok := index[family+"\x00"+first]
		b, bok := index[family+"\x00"+second]
		if !aok || !bok {
			return
		}
		facts[a].Issue = joinIssue(facts[a].Issue, message)
		facts[b].Issue = joinIssue(facts[b].Issue, message)
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT a.id,b.id FROM planning_events a JOIN planning_events b ON b.household_id=a.household_id AND b.id>a.id JOIN planning_event_owners ao ON ao.event_id=a.id JOIN planning_event_owners bo ON bo.event_id=b.id AND bo.user_id=ao.user_id WHERE a.household_id=? AND a.active=1 AND b.active=1 AND a.status='planned' AND b.status='planned' AND COALESCE(NULLIF(a.starts_on,''),a.starts_at)<COALESCE(date(NULLIF(b.ends_on,''),'+1 day'),date(NULLIF(b.starts_on,''),'+1 day'),NULLIF(b.ends_at,'')) AND COALESCE(NULLIF(b.starts_on,''),b.starts_at)<COALESCE(date(NULLIF(a.ends_on,''),'+1 day'),date(NULLIF(a.starts_on,''),'+1 day'),NULLIF(a.ends_at,''))`, householdID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var first, second string
		if err := rows.Scan(&first, &second); err != nil {
			rows.Close()
			return err
		}
		mark("planning", first, second, "assigned owners have overlapping events")
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	return rows.Close()
}

func applyHealthConflicts(facts []Fact, conflicts []health.Conflict) {
	byRecord := make(map[string]int, len(facts))
	for index, fact := range facts {
		byRecord[fact.RecordID] = index
	}
	for _, conflict := range conflicts {
		if index, ok := byRecord[conflict.RecordID]; ok {
			reason := conflict.Reason
			if strings.HasPrefix(reason, "Units ") {
				reason = "reported units differ and cannot be compared; enter the correct value and unit."
			}
			facts[index].Issue = joinIssue(facts[index].Issue, reason)
		}
	}
}

func coachingFacts(facts []Fact) []Fact {
	out := make([]Fact, 0, len(facts))
	for _, fact := range facts {
		if fact.Family == "health" && fact.Issue != "" {
			continue
		}
		out = append(out, fact)
	}
	return out
}

func joinIssue(current, next string) string {
	if current == "" {
		return next
	}
	if strings.Contains(current, next) {
		return current
	}
	return current + "; " + next
}

func weeklyScope(context Context, asOf time.Time) ReviewScope {
	if asOf.IsZero() {
		asOf = time.Now().UTC()
	}
	asOf = asOf.UTC()
	facts := context.ReviewFacts
	if facts == nil {
		facts = context.Facts
	}
	scope := ReviewScope{Context: context, Insights: deterministic(facts, asOf, context.Signals...).Insights}
	for _, fact := range facts {
		event := reviewEvent(fact)
		event.Overdue = reviewFactOverdue(fact, asOf)
		switch weeklySection(fact, asOf) {
		case "issue":
			event.Title, event.Copy = reviewIssueCopy(fact)
			scope.Issues = append(scope.Issues, event)
		case "upcoming":
			event.Copy, event.Reason, event.NextStep = upcomingCopy(fact)
			scope.Upcoming = append(scope.Upcoming, event)
		case "change":
			event.Copy = changeCopy(fact, asOf)
			scope.Changes = append(scope.Changes, event)
		}
	}
	scope.Issues = mergeReviewIssues(scope.Issues)
	scope.Upcoming = groupReviewUpcoming(scope.Upcoming)
	sortReviewEvents(scope.Issues, false)
	sortReviewEvents(scope.Upcoming, true)
	sortReviewEvents(scope.Changes, false)
	scope.Issues = limitReviewEvents(scope.Issues, 6)
	scope.Upcoming = limitReviewEvents(scope.Upcoming, 6)
	scope.Changes = limitReviewEvents(scope.Changes, 6)
	scope.Priorities = reviewPriorities(scope)
	scope.Progress = reviewProgress(scope)
	scope.Observation = reviewObservation(context.Signals)
	scope.Status = reviewStatus(scope)
	scope.Insights = reviewInsights(scope.Insights, scope)
	return scope
}

func weeklySection(f Fact, asOf time.Time) string {
	if f.Issue != "" {
		return "issue"
	}
	today := day(asOf)
	start := today.AddDate(0, 0, -6)
	eventDate, hasDate := time.Parse("2006-01-02", f.Date)
	if f.SupersedesID != "" && !f.CreatedAt.Before(start) {
		return "change"
	}
	if f.Status == "completed" || f.Status == "cancelled" {
		if f.UpdatedAt.After(f.CreatedAt) && !f.UpdatedAt.Before(start) {
			return "change"
		}
		if hasDate == nil && !eventDate.Before(start) && !f.CreatedAt.Before(start) {
			return "change"
		}
		return ""
	}
	if f.UpdatedAt.After(f.CreatedAt) && !f.UpdatedAt.Before(start) {
		return "change"
	}
	if hasDate == nil && actionableReviewFact(f) {
		if openStatus(f.Status) && !eventDate.Before(today) && eventDate.Before(today.AddDate(0, 0, 31)) {
			return "upcoming"
		}
		if eventDate.Before(today) && openStatus(f.Status) {
			return "change"
		}
	}
	return ""
}

func actionableReviewFact(f Fact) bool {
	switch f.Family {
	case "finance":
		return f.Kind == "obligation"
	case "health":
		return f.Kind == "appointment" || f.Kind == "routine"
	case "planning":
		return f.Kind == "goal" || f.Kind == "milestone" || f.Kind == "event"
	default:
		return false
	}
}

func reviewFactOverdue(f Fact, asOf time.Time) bool {
	if !actionableReviewFact(f) || !openStatus(f.Status) {
		return false
	}
	date, err := time.Parse("2006-01-02", f.Date)
	return err == nil && date.Before(day(asOf))
}

func reviewInsights(items []Item, scope ReviewScope) []Item {
	used := make(map[string]struct{}, len(scope.Changes)+len(scope.Upcoming)+len(scope.Issues))
	for _, events := range [][]ReviewEvent{scope.Changes, scope.Upcoming, scope.Issues} {
		for _, event := range events {
			for _, evidenceID := range event.evidenceIDs() {
				used[evidenceID] = struct{}{}
			}
		}
	}
	out := make([]Item, 0, len(items))
	for _, item := range items {
		duplicate := false
		for _, evidence := range item.EvidenceIDs {
			if _, exists := used[evidence]; exists {
				duplicate = true
				break
			}
		}
		if !duplicate {
			item.Title = reviewInsightTitle(item)
			item.Copy = reviewInsightCopy(item)
			out = append(out, item)
		}
	}
	return out
}

// Cached model wording stays inside a collapsed disclosure. The deterministic
// weekly sections remain the primary plan even when the same evidence helped
// the model phrase an observation.
func cachedReviewInsights(items []Item) []Item {
	out := make([]Item, 0, len(items))
	for _, item := range items {
		item.Title = reviewInsightTitle(item)
		item.Copy = reviewInsightCopy(item)
		out = append(out, item)
	}
	return out
}

func reviewInsightCopy(item Item) string {
	if item.Title == "Health record comparison" || strings.Contains(item.Copy, " changed from ") {
		if before, after, found := strings.Cut(item.Copy, " changed from "); found {
			if measurement, _, hasSubject := strings.Cut(before, " for "); hasSubject {
				return measurement + " changed from " + after
			}
		}
	}
	return item.Copy
}

func reviewInsightTitle(item Item) string {
	if item.Title != "Health record comparison" {
		return item.Title
	}
	name := strings.TrimSpace(strings.Split(item.Copy, " changed from ")[0])
	if before, _, found := strings.Cut(name, " for "); found {
		name = before
	}
	if name != "" {
		return name
	}
	return item.Title
}

func reviewEvent(f Fact) ReviewEvent {
	return ReviewEvent{
		Fact:        f,
		Facts:       []Fact{f},
		Title:       reviewTitle(f),
		When:        f.Date,
		Time:        f.Time,
		Domain:      reviewDomain(f),
		Visibility:  reviewVisibility(f.Visibility),
		Status:      reviewStatusLabel(f.Status),
		EvidenceID:  f.EvidenceID,
		EvidenceIDs: []string{f.EvidenceID},
	}
}

func (e ReviewEvent) evidenceIDs() []string {
	if len(e.EvidenceIDs) > 0 {
		return e.EvidenceIDs
	}
	if e.EvidenceID != "" {
		return []string{e.EvidenceID}
	}
	return nil
}

func reviewTitle(f Fact) string {
	title := strings.TrimSpace(f.Content)
	if f.Family == "health" && f.Kind == "observation" {
		title = strings.TrimSpace(strings.Split(title, " for ")[0])
	}
	words := strings.Fields(title)
	if len(words) > 1 && strings.EqualFold(words[0], words[len(words)-1]) {
		words = words[:len(words)-1]
	}
	return strings.Join(words, " ")
}

func reviewDomain(f Fact) string {
	switch f.Family {
	case "finance":
		return "Finance"
	case "health":
		return "Health"
	case "planning":
		return "Planning"
	default:
		return "Household"
	}
}

func reviewVisibility(v policy.Visibility) string {
	if v == policy.Personal {
		return "Only you"
	}
	return "Shared"
}

func reviewStatusLabel(status string) string {
	switch status {
	case "planned":
		return "Planned"
	case "pending":
		return "Pending"
	case "active":
		return "Active"
	case "completed":
		return "Completed"
	case "cancelled":
		return "Cancelled"
	default:
		return "Recorded"
	}
}

func reviewIssueCopy(f Fact) (string, string) {
	issue := strings.TrimSuffix(strings.TrimSpace(f.Issue), ".")
	if f.Family == "health" && f.Kind == "observation" && strings.Contains(issue, "reported units differ") {
		return reviewTitle(f) + " record needs correction", "Mithra cannot compare two readings because their recorded units differ."
	}
	return reviewTitle(f) + " needs correction", "Mithra cannot use this record yet: " + issue + "."
}

func upcomingCopy(f Fact) (copy, reason, next string) {
	switch {
	case f.Family == "finance" && f.Kind == "obligation":
		return "A recorded payment is due.", "A recorded payment is due soon.", "Check the payment details before its due date."
	case f.Family == "planning":
		return "Preparation is due soon.", "This household plan is coming up.", "Review what needs to be ready before then."
	case f.Family == "health":
		return "A recorded health follow-up is due.", "This follow-up has a recorded date.", "Review the record when you are ready."
	default:
		return "A recorded item is coming up.", "This record has a date soon.", "Review the record before then."
	}
}

func mergeReviewIssues(events []ReviewEvent) []ReviewEvent {
	byKey := make(map[string]int, len(events))
	out := make([]ReviewEvent, 0, len(events))
	for _, event := range events {
		key := event.Fact.Family + "\x00" + event.Fact.Kind + "\x00" + strings.ToLower(event.Fact.Content) + "\x00" + event.Copy
		if index, ok := byKey[key]; ok {
			out[index] = mergeReviewEvents(out[index], event)
			continue
		}
		byKey[key] = len(out)
		out = append(out, event)
	}
	return out
}

func groupReviewUpcoming(events []ReviewEvent) []ReviewEvent {
	used := make([]bool, len(events))
	out := make([]ReviewEvent, 0, len(events))
	for i, event := range events {
		if used[i] || event.Fact.Family != "planning" {
			continue
		}
		j, ok := uniqueClosestReviewMatch(events, used, i)
		if !ok {
			continue
		}
		if matchingPlanning, unique := uniqueClosestPlanningMatch(events, used, j); !unique || matchingPlanning != i {
			continue
		}
		candidate := events[j]
		combined := mergeReviewEvents(candidate, event)
		combined.Title = candidate.Title
		combined.When, combined.Time = earliestReviewWhen(event, candidate)
		combined.Domain = "Planning + finance"
		combined.Status = "Planned · Pending"
		combined.Copy = "Review options by " + displayDate(event.When) + " · Payment due " + displayDate(candidate.When) + "."
		combined.Reason = combined.Copy
		combined.NextStep = "Review the options before the payment is due."
		out = append(out, combined)
		used[i], used[j] = true, true
	}
	for i, event := range events {
		if !used[i] {
			out = append(out, event)
		}
	}
	return out
}

func uniqueClosestPlanningMatch(events []ReviewEvent, used []bool, obligationIndex int) (int, bool) {
	obligation := events[obligationIndex]
	best, bestDays, tied := -1, 4, false
	for index, planning := range events {
		if used[index] || planning.Fact.Family != "planning" || planning.Fact.Visibility != obligation.Fact.Visibility || !reviewTitlesMatch(planning.Title, obligation.Title) || !reviewDatesClose(planning.When, obligation.When) {
			continue
		}
		left, leftErr := time.Parse("2006-01-02", planning.When)
		right, rightErr := time.Parse("2006-01-02", obligation.When)
		if leftErr != nil || rightErr != nil {
			continue
		}
		days := int(left.Sub(right).Hours() / 24)
		if days < 0 {
			days = -days
		}
		if days < bestDays {
			best, bestDays, tied = index, days, false
		} else if days == bestDays {
			tied = true
		}
	}
	return best, best >= 0 && !tied
}

func uniqueClosestReviewMatch(events []ReviewEvent, used []bool, planningIndex int) (int, bool) {
	planning := events[planningIndex]
	best, bestDays, tied := -1, 4, false
	for index, candidate := range events {
		if used[index] || candidate.Fact.Family != "finance" || candidate.Fact.Kind != "obligation" || planning.Fact.Visibility != candidate.Fact.Visibility || !reviewTitlesMatch(planning.Title, candidate.Title) || !reviewDatesClose(planning.When, candidate.When) {
			continue
		}
		left, leftErr := time.Parse("2006-01-02", planning.When)
		right, rightErr := time.Parse("2006-01-02", candidate.When)
		if leftErr != nil || rightErr != nil {
			continue
		}
		days := int(left.Sub(right).Hours() / 24)
		if days < 0 {
			days = -days
		}
		if days < bestDays {
			best, bestDays, tied = index, days, false
		} else if days == bestDays {
			tied = true
		}
	}
	return best, best >= 0 && !tied
}

func mergeReviewEvents(left, right ReviewEvent) ReviewEvent {
	left.Facts = append(append([]Fact(nil), left.Facts...), right.Facts...)
	left.EvidenceIDs = append(append([]string(nil), left.evidenceIDs()...), right.evidenceIDs()...)
	if left.EvidenceID == "" {
		left.EvidenceID = right.EvidenceID
	}
	return left
}

func reviewTitlesMatch(left, right string) bool {
	normalize := func(value string) string {
		words := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return r < 'a' || r > 'z' })
		out := words[:0]
		for _, word := range words {
			if word != "review" {
				out = append(out, word)
			}
		}
		return strings.Join(out, " ")
	}
	return normalize(left) != "" && normalize(left) == normalize(right)
}

func reviewDatesClose(left, right string) bool {
	a, errA := time.Parse("2006-01-02", left)
	b, errB := time.Parse("2006-01-02", right)
	if errA != nil || errB != nil {
		return false
	}
	if a.After(b) {
		a, b = b, a
	}
	return b.Sub(a) <= 72*time.Hour
}

func earliestReviewWhen(left, right ReviewEvent) (string, string) {
	if left.When <= right.When {
		return left.When, left.Time
	}
	return right.When, right.Time
}

func reviewPriorities(scope ReviewScope) []ReviewEvent {
	all := append([]ReviewEvent(nil), scope.Issues...)
	for _, event := range scope.Changes {
		if event.Overdue {
			all = append(all, event)
		}
	}
	all = append(all, scope.Upcoming...)
	sort.SliceStable(all, func(i, j int) bool {
		left, right := reviewPriorityRank(all[i]), reviewPriorityRank(all[j])
		if left != right {
			return left < right
		}
		return all[i].When < all[j].When
	})
	if len(all) > 3 {
		all = all[:3]
	}
	for index := range all {
		if all[index].Reason == "" {
			all[index].Reason = all[index].Copy
		}
		if all[index].NextStep == "" {
			all[index].NextStep = "Review the record when you are ready."
		}
	}
	return all
}

func reviewPriorityRank(event ReviewEvent) int {
	if event.Fact.Issue != "" {
		return 0
	}
	if event.Overdue {
		return 1
	}
	if event.Fact.Family == "planning" || event.Fact.Kind == "obligation" || event.Domain == "Planning + finance" {
		return 2
	}
	return 3
}

func reviewProgress(scope ReviewScope) []ReviewEvent {
	progress := make([]ReviewEvent, 0, 3)
	for _, event := range scope.Changes {
		if event.Fact.Status == "completed" {
			event.Copy = "Marked completed this week."
			progress = append(progress, event)
		}
	}
	for _, signal := range scope.Context.Signals {
		if signal.Kind != "finance_month_to_date" || !spendingLowerThanPrior(signal.Summary) {
			continue
		}
		progress = append(progress, ReviewEvent{Title: "Spending compared with last month", Copy: signal.Summary, Domain: "Finance", Visibility: reviewVisibility(policy.Visibility(scope.Context.Scope)), Status: "Recorded", EvidenceIDs: signal.EvidenceIDs})
		break
	}
	if len(progress) > 3 {
		return progress[:3]
	}
	return progress
}

func spendingLowerThanPrior(summary string) bool {
	match := regexp.MustCompile(` is ([0-9][0-9,]*(?:\.[0-9]+)?), compared with ([0-9][0-9,]*(?:\.[0-9]+)?) from`).FindStringSubmatch(summary)
	if len(match) != 3 {
		return false
	}
	current, currentErr := strconv.ParseFloat(strings.ReplaceAll(match[1], ",", ""), 64)
	prior, priorErr := strconv.ParseFloat(strings.ReplaceAll(match[2], ",", ""), 64)
	return currentErr == nil && priorErr == nil && current < prior
}

func reviewObservation(signals []Signal) Item {
	var budget, month Signal
	for _, signal := range signals {
		switch signal.Kind {
		case "finance_budget":
			budget = signal
		case "finance_month_to_date":
			month = signal
		}
	}
	if budget.Kind == "" || !hasBudgetRisk([]Signal{budget}) {
		return Item{}
	}
	category := "This budget"
	if match := regexp.MustCompile(`Spending recorded in ([^.]+) through`).FindStringSubmatch(budget.Summary); len(match) == 2 {
		category = match[1]
	}
	title := category + " need attention"
	copy := budget.Summary
	if month.Kind != "" {
		copy += " " + month.Summary
		if spendingLowerThanPrior(month.Summary) {
			title = category + " need attention, not overall spending"
		}
	}
	if remaining := budgetRemaining(budget.Summary); remaining != "" {
		copy += " Suggested next step: Keep the remaining recorded " + strings.ToLower(category) + " spending within " + remaining + ", or adjust the recorded budget."
	}
	evidence := append([]string(nil), budget.EvidenceIDs...)
	for _, id := range month.EvidenceIDs {
		if !containsString(evidence, id) {
			evidence = append(evidence, id)
		}
	}
	return Item{Title: title, Copy: copy, EvidenceIDs: evidence}
}

func budgetRemaining(summary string) string {
	match := regexp.MustCompile(`leaving ([0-9][0-9,]*(?:\.[0-9]+)?)\.`).FindStringSubmatch(summary)
	if len(match) == 2 {
		return match[1]
	}
	return ""
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func reviewStatus(scope ReviewScope) ReviewStatus {
	if scope.Context.Scope == string(policy.Personal) {
		if len(scope.Issues) > 0 {
			return ReviewStatus{Label: "Needs attention", Copy: "A private record needs correction before Mithra can use it."}
		}
		return ReviewStatus{Label: "Up to date", Copy: "No private record needs correction right now."}
	}
	if len(scope.Context.ReviewFacts) == 0 && len(scope.Context.Facts) == 0 && len(scope.Context.Signals) == 0 {
		return ReviewStatus{Label: "No shared records yet", Copy: "Add or import shared records to build a weekly status."}
	}
	if len(scope.Issues) > 0 {
		copy := "A visible record needs correction before Mithra can use it."
		if category := budgetRiskCategory(scope.Context.Signals); category != "" {
			copy += " " + category + " spending is close to the recorded budget."
		}
		if next := reviewPriorityNames(scope.Priorities); next != "" {
			copy += " Next: " + next + "."
		}
		return ReviewStatus{Label: "Needs attention", Copy: copy}
	}
	for _, event := range scope.Changes {
		if event.Overdue {
			return ReviewStatus{Label: "Needs attention", Copy: event.Title + " is past its recorded date and still open."}
		}
	}
	if len(scope.Priorities) > 0 || hasBudgetRisk(scope.Context.Signals) {
		parts := make([]string, 0, 3)
		for _, event := range scope.Priorities {
			parts = append(parts, event.Title)
		}
		copy := "A few recorded items need attention next."
		if len(parts) > 0 {
			copy = "Next: " + joinReviewNames(parts) + "."
		}
		if category := budgetRiskCategory(scope.Context.Signals); category != "" {
			copy = category + " spending is close to the recorded budget. " + copy
		}
		return ReviewStatus{Label: "Mostly on track", Copy: strings.ToUpper(copy[:1]) + copy[1:]}
	}
	return ReviewStatus{Label: "On track", Copy: "No shared overdue item or budget risk is recorded for this week."}
}

func reviewPriorityNames(events []ReviewEvent) string {
	parts := make([]string, 0, len(events))
	for _, event := range events {
		parts = append(parts, event.Title)
	}
	return joinReviewNames(parts)
}

func joinReviewNames(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + ", and " + parts[len(parts)-1]
	}
}

func hasBudgetRisk(signals []Signal) bool {
	for _, signal := range signals {
		if signal.Kind != "finance_budget" {
			continue
		}
		match := regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)% of the recorded budget`).FindStringSubmatch(signal.Summary)
		if len(match) != 2 {
			continue
		}
		percent, err := strconv.ParseFloat(match[1], 64)
		if err == nil && percent >= 80 {
			return true
		}
	}
	return false
}

func budgetRiskCategory(signals []Signal) string {
	for _, signal := range signals {
		if signal.Kind != "finance_budget" || !hasBudgetRisk([]Signal{signal}) {
			continue
		}
		match := regexp.MustCompile(`Spending recorded in ([^.]+) through`).FindStringSubmatch(signal.Summary)
		if len(match) == 2 {
			return match[1]
		}
	}
	return ""
}

func displayDate(value string) string {
	date, err := time.Parse("2006-01-02", value)
	if err != nil {
		return value
	}
	return date.Format("2 Jan 2006")
}

func openStatus(status string) bool {
	return status == "" || status == "planned" || status == "pending" || status == "active"
}

func changeCopy(f Fact, asOf time.Time) string {
	switch {
	case f.SupersedesID != "":
		return "Corrected this week."
	case f.Status == "completed":
		return "Marked completed this week."
	case f.Status == "cancelled":
		return "Marked cancelled this week."
	case f.UpdatedAt.After(f.CreatedAt):
		return "Updated this week."
	}
	if eventDate, err := time.Parse("2006-01-02", f.Date); err == nil && eventDate.Before(day(asOf)) && openStatus(f.Status) {
		return "Past its recorded date and still open."
	}
	return "Recorded for " + displayDate(f.Date) + "."
}

func sortReviewEvents(events []ReviewEvent, ascending bool) {
	sort.Slice(events, func(i, j int) bool {
		if ascending {
			return events[i].When < events[j].When
		}
		left, right := events[i].Fact.UpdatedAt, events[j].Fact.UpdatedAt
		if left.Equal(right) {
			return events[i].Title < events[j].Title
		}
		return left.After(right)
	})
}

func limitReviewEvents(events []ReviewEvent, max int) []ReviewEvent {
	if len(events) > max {
		return events[:max]
	}
	return events
}

func deterministic(facts []Fact, asOf time.Time, signals ...Signal) Narrative {
	if asOf.IsZero() {
		asOf = time.Now().UTC()
	}
	asOf = asOf.UTC()
	out := Narrative{}
	copyFacts := append([]Fact(nil), facts...)
	sort.Slice(copyFacts, func(i, j int) bool { return copyFacts[i].CreatedAt.After(copyFacts[j].CreatedAt) })
	for _, f := range copyFacts {
		item := factItem(f)
		if out.Lead.Title == "" {
			out.Lead = item
		}
		if !f.CreatedAt.Before(asOf.AddDate(0, 0, -7)) {
			out.Changes = append(out.Changes, item)
		}
		if f.Issue != "" {
			issue := item
			issue.Copy = "Recorded issue: " + f.Issue + "."
			out.Inconsistencies = append(out.Inconsistencies, issue)
		}
		if d, err := time.Parse("2006-01-02", f.Date); err == nil && !d.Before(day(asOf)) && d.Before(day(asOf).AddDate(0, 0, 31)) {
			item.When = f.Date
			out.Dates = append(out.Dates, item)
		}
	}
	sort.Slice(out.Dates, func(i, j int) bool { return out.Dates[i].When < out.Dates[j].When })
	for _, list := range [][]Item{out.Inconsistencies, out.Dates, out.Changes} {
		for _, item := range list {
			if len(out.Priorities) == 3 {
				break
			}
			if !containsEvidence(out.Priorities, item.EvidenceIDs[0]) {
				out.Priorities = append(out.Priorities, item)
			}
		}
	}
	if len(out.Changes) > 6 {
		out.Changes = out.Changes[:6]
	}
	if len(out.Dates) > 6 {
		out.Dates = out.Dates[:6]
	}
	if len(out.Inconsistencies) > 6 {
		out.Inconsistencies = out.Inconsistencies[:6]
	}
	for _, signal := range signals {
		if len(out.Insights) == 5 {
			break
		}
		item := Item{Title: signalTitle(signal.Kind), Copy: signal.Summary, When: signal.Period, EvidenceIDs: signal.EvidenceIDs}
		out.Insights = append(out.Insights, item)
	}
	return out
}

func signalTitle(kind string) string {
	switch kind {
	case "finance_month_to_date":
		return "Month-to-date spending"
	case "finance_budget":
		return "Budget and spending"
	case "finance_obligations":
		return "Upcoming obligations"
	case "health_series":
		return "Health record comparison"
	case "planning_upcoming":
		return "Plans in the next month"
	default:
		return "Recorded pattern"
	}
}

func withSignals(n Narrative, signals []Signal) Narrative {
	for _, signal := range signals {
		item := Item{Title: signalTitle(signal.Kind), Copy: signal.Summary, When: signal.Period, EvidenceIDs: signal.EvidenceIDs}
		if len(n.Insights) < 5 && !hasEvidence(n.Insights, item.EvidenceIDs) {
			n.Insights = append(n.Insights, item)
		}
	}
	return n
}

func hasEvidence(items []Item, evidence []string) bool {
	for _, item := range items {
		if strings.Join(item.EvidenceIDs, "\x00") == strings.Join(evidence, "\x00") {
			return true
		}
	}
	return false
}

func factItem(f Fact) Item {
	copy := "Recorded in " + f.Family + "."
	if f.Date != "" {
		copy = "Recorded for " + f.Date + " in " + f.Family + "."
	}
	return Item{Title: f.Content, Copy: copy, When: f.Date, EvidenceIDs: []string{f.EvidenceID}}
}

func validateNarrative(n Narrative, allowed map[string]Fact, signals ...Signal) error {
	if err := validateNarrativeShape(n); err != nil {
		return err
	}
	for _, item := range narrativeItems(n) {
		if err := validateItem(item, allowed, signals...); err != nil {
			return err
		}
	}
	return nil
}

func sanitizeNarrative(n Narrative, allowed map[string]Fact, fallback Item, signals ...Signal) (Narrative, error) {
	if err := validateNarrativeShape(n); err != nil {
		return Narrative{}, err
	}
	var out Narrative
	modelItemKept := false
	if !emptyItem(n.Lead) && validateItem(n.Lead, allowed, signals...) == nil {
		out.Lead = n.Lead
		modelItemKept = true
	}
	keep := func(items []Item, normalizeSignalEvidence bool) []Item {
		out := make([]Item, 0, len(items))
		for _, item := range items {
			if normalizeSignalEvidence {
				item = normalizeSignalInsight(item, signals)
			}
			if emptyItem(item) || validateItem(item, allowed, signals...) != nil {
				continue
			}
			out = append(out, item)
			modelItemKept = true
		}
		return out
	}
	out.Insights = keep(n.Insights, true)
	out.Changes = keep(n.Changes, false)
	out.Dates = keep(n.Dates, false)
	out.Inconsistencies = keep(n.Inconsistencies, false)
	out.Priorities = keep(n.Priorities, false)
	if !modelItemKept {
		return Narrative{}, ErrUnsupported
	}
	if len(signals) > 0 && !hasExactSignalInsight(out.Insights, signals) {
		signal := signals[0]
		item := Item{Title: signalTitle(signal.Kind), Copy: signal.Summary, When: signal.Period, EvidenceIDs: append([]string(nil), signal.EvidenceIDs...)}
		if len(out.Insights) == 5 {
			out.Insights[4] = item
		} else {
			out.Insights = append(out.Insights, item)
		}
	}
	if len(out.Insights) == 0 {
		return Narrative{}, ErrUnsupported
	}
	if emptyItem(out.Lead) {
		if emptyItem(fallback) {
			return Narrative{}, ErrUnsupported
		}
		out.Lead = fallback
	}
	return out, nil
}

func normalizeSignalInsight(item Item, signals []Signal) Item {
	matched := -1
	for index, signal := range signals {
		if item.Copy != signal.Summary || !signalEvidenceSubset(item.EvidenceIDs, signal.EvidenceIDs) {
			continue
		}
		if matched >= 0 {
			return item
		}
		matched = index
	}
	if matched >= 0 {
		item.Title = signalTitle(signals[matched].Kind)
		item.When = signals[matched].Period
		item.EvidenceIDs = append([]string(nil), signals[matched].EvidenceIDs...)
	}
	return item
}

func signalEvidenceSubset(ids, signalIDs []string) bool {
	if len(ids) == 0 || len(ids) > len(signalIDs) {
		return false
	}
	allowed := make(map[string]struct{}, len(signalIDs))
	for _, id := range signalIDs {
		allowed[id] = struct{}{}
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := allowed[id]; !ok {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func hasExactSignalInsight(insights []Item, signals []Signal) bool {
	for _, insight := range insights {
		for _, signal := range signals {
			if insight.Copy == signal.Summary && strings.Join(insight.EvidenceIDs, "\x00") == strings.Join(signal.EvidenceIDs, "\x00") {
				return true
			}
		}
	}
	return false
}

func validateNarrativeShape(n Narrative) error {
	if len(n.Insights) > 5 || len(n.Priorities) > 3 || len(n.Changes) > 12 || len(n.Dates) > 12 || len(n.Inconsistencies) > 12 {
		return ErrInvalid
	}
	return nil
}

func narrativeItems(n Narrative) []Item {
	items := []Item{n.Lead}
	items = append(items, n.Insights...)
	items = append(items, n.Changes...)
	items = append(items, n.Dates...)
	items = append(items, n.Inconsistencies...)
	return append(items, n.Priorities...)
}

func emptyItem(item Item) bool {
	return strings.TrimSpace(item.Title) == "" && strings.TrimSpace(item.Copy) == ""
}

func validateItem(item Item, allowed map[string]Fact, signals ...Signal) error {
	if emptyItem(item) {
		return nil
	}
	if len(item.Title) > 256 || len(item.Copy) > 1200 || len(item.EvidenceIDs) == 0 || len(item.EvidenceIDs) > 12 {
		return ErrUnsupported
	}
	var cited []Fact
	for _, id := range item.EvidenceIDs {
		fact, ok := allowed[id]
		if !ok {
			return ErrUnsupported
		}
		cited = append(cited, fact)
	}
	if canonicalSignalInsight(item, signals) {
		return nil
	}
	if imperativeWording(item.Title) || imperativeWording(item.Copy) || unsafeWording(item.Title+" "+item.Copy, fullyCitesSignal(item.EvidenceIDs, signals)) {
		return ErrUnsupported
	}
	if !groundedWording(item.Title+" "+item.Copy+" "+item.When, cited, signals, item.EvidenceIDs) {
		return ErrUnsupported
	}
	return nil
}

func canonicalSignalInsight(item Item, signals []Signal) bool {
	for _, signal := range signals {
		if item.Title == signalTitle(signal.Kind) && item.Copy == signal.Summary && item.When == signal.Period && strings.Join(item.EvidenceIDs, "\x00") == strings.Join(signal.EvidenceIDs, "\x00") {
			return true
		}
	}
	return false
}

func unsafeWording(value string, allowComparison bool) bool {
	lower := " " + strings.ToLower(value) + " "
	for _, term := range []string{" because ", " caused ", " causes ", " leads to ", " diagnosis ", " diagnose ", " treatment ", " medication ", " should take ", " you should ", " we recommend ", " seek medical ", " consult a ", " at risk ", " normal range ", " abnormal ", " unhealthy ", " blame ", " fault ", " score ", " reset ", " overdue "} {
		if strings.Contains(lower, term) {
			return true
		}
	}
	if !allowComparison {
		for _, term := range []string{" increased ", " decreased ", " higher ", " lower ", " rising ", " falling ", " rose ", " fell ", " dropped ", " grew ", " declined ", " more ", " less "} {
			if strings.Contains(lower, term) {
				return true
			}
		}
	}
	return false
}

func imperativeWording(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	if lower == "" {
		return false
	}
	for _, phrase := range []string{"needs action", "action needed", "should", "please", "need to", "needs to", "must", "have to", "required to"} {
		if containsPhrase(lower, phrase) {
			return true
		}
	}
	for _, command := range imperativeCommands {
		if startsCommand(lower, command) {
			return true
		}
	}
	for _, prefix := range []string{"could you ", "would you ", "can you ", "will you "} {
		if strings.HasPrefix(lower, prefix) && startsAnyCommand(strings.TrimSpace(strings.TrimPrefix(lower, prefix))) {
			return true
		}
	}
	return false
}

var imperativeCommands = []string{"check", "review", "consider", "remember", "make sure", "follow up", "follow-up", "contact", "call", "book", "pay", "update", "confirm", "buy", "see", "sell", "take"}

func startsAnyCommand(value string) bool {
	for _, command := range imperativeCommands {
		if startsCommand(value, command) {
			return true
		}
	}
	return false
}

func startsCommand(value, command string) bool {
	return value == command || strings.HasPrefix(value, command+" ") || strings.HasPrefix(value, command+":")
}

func containsPhrase(value, phrase string) bool {
	return strings.Contains(" "+value+" ", " "+phrase+" ") || strings.Contains(value, phrase+".") || strings.Contains(value, phrase+",") || strings.Contains(value, phrase+":")
}

func fullyCitesSignal(citedIDs []string, signals []Signal) bool {
	cited := make(map[string]struct{}, len(citedIDs))
	for _, id := range citedIDs {
		cited[id] = struct{}{}
	}
	for _, signal := range signals {
		if len(signal.EvidenceIDs) == 0 {
			continue
		}
		complete := true
		for _, id := range signal.EvidenceIDs {
			if _, ok := cited[id]; !ok {
				complete = false
				break
			}
		}
		if complete {
			return true
		}
	}
	return false
}

var coachingNumber = regexp.MustCompile(`[+-]?[0-9]+(?:[.,][0-9]+)*`)
var coachingWord = regexp.MustCompile(`[a-zA-Z]{4,}`)

func groundedWording(wording string, facts []Fact, signals []Signal, citedIDs []string) bool {
	if len(facts) == 0 {
		return false
	}
	var evidence strings.Builder
	for _, fact := range facts {
		evidence.WriteString(" ")
		evidence.WriteString(strings.ToLower(fact.Content))
		evidence.WriteString(" ")
		evidence.WriteString(strings.ToLower(fact.Date))
		evidence.WriteString(" ")
		evidence.WriteString(strings.ToLower(fact.Issue))
	}
	cited := make(map[string]struct{}, len(citedIDs))
	for _, id := range citedIDs {
		cited[id] = struct{}{}
	}
	for _, signal := range signals {
		if len(signal.EvidenceIDs) == 0 {
			continue
		}
		allCited := true
		for _, id := range signal.EvidenceIDs {
			if _, ok := cited[id]; !ok {
				allCited = false
				break
			}
		}
		if allCited {
			evidence.WriteString(" ")
			evidence.WriteString(strings.ToLower(signal.Summary))
			evidence.WriteString(" ")
			evidence.WriteString(strings.ToLower(signal.Period))
		}
	}
	source := evidence.String()
	lower := strings.ToLower(wording)
	for _, number := range coachingNumber.FindAllString(lower, -1) {
		if !containsNumber(source, number) {
			return false
		}
	}
	return allSubstantiveTermsGrounded(lower, source, fullyCitesSignal(citedIDs, signals))
}

func allSubstantiveTermsGrounded(wording, source string, allowComparison bool) bool {
	sourceTerms := make(map[string]struct{})
	for _, word := range coachingWord.FindAllString(source, -1) {
		sourceTerms[word] = struct{}{}
	}
	for _, word := range coachingWord.FindAllString(wording, -1) {
		if blockedCoachingWord[word] {
			return false
		}
		if comparisonCoachingWord[word] {
			if !allowComparison {
				return false
			}
			continue
		}
		if neutralCoachingWord[word] {
			continue
		}
		if _, ok := sourceTerms[word]; !ok {
			return false
		}
	}
	return true
}

var neutralCoachingWord = map[string]bool{
	"about": true, "activity": true, "after": true, "alongside": true,
	"appear": true, "appears": true, "been": true, "before": true, "between": true,
	"calendar": true, "check": true, "checking": true, "comparison": true, "confirm": true,
	"confirmation": true, "could": true, "current": true, "date": true, "dates": true,
	"december": true,
	"detail":   true, "details": true, "during": true, "each": true, "february": true,
	"finance": true, "from": true, "have": true, "health": true, "household": true,
	"entries": true, "entry": true, "individual": true, "information": true, "into": true,
	"item": true, "items": true, "issue": true, "january": true, "july": true, "june": true,
	"last": true, "listed": true, "listing": true, "march": true, "mixed": true,
	"may": true, "might": true, "month": true, "months": true, "monthly": true,
	"next": true, "november": true, "october": true, "pattern": true,
	"patterns": true, "period": true, "periods": true, "planning": true, "plans": true,
	"prior": true, "private": true, "recent": true, "record": true, "recorded": true,
	"records": true, "repeated": true, "schedule": true, "scheduled": true, "shared": true,
	"source": true, "sources": true, "still": true,
	"that": true, "their": true, "there": true, "these": true, "this": true,
	"those": true, "today": true, "tomorrow": true, "update": true, "updates": true,
	"together": true, "total": true, "totals": true, "upcoming": true, "value": true,
	"values": true, "view": true, "visible": true, "week": true, "weeks": true,
	"were": true, "will": true, "with": true, "worth": true, "would": true,
	"year": true, "years": true, "yesterday": true, "your": true,
}

var comparisonCoachingWord = map[string]bool{
	"against": true, "comparison": true, "compared": true, "decreased": true, "declined": true,
	"dropped": true, "falling": true, "fell": true, "grew": true, "higher": true,
	"increased": true, "less": true, "lower": true, "more": true, "rising": true,
	"rose": true,
}

var blockedCoachingWord = map[string]bool{
	"abnormal": true, "advice": true, "advise": true, "always": true, "assured": true,
	"better": true, "certain": true, "certainly": true, "clear": true, "clearly": true,
	"critical": true, "danger": true, "dangerous": true, "definitely": true, "emergency": true,
	"ensure": true, "guaranteed": true, "healthy": true, "immediate": true, "immediately": true,
	"important": true, "improve": true, "improved": true, "improvement": true, "likely": true,
	"normal": true, "possibly": true, "probably": true, "recommend": true, "recommended": true,
	"recommendation": true, "risk": true, "risky": true, "safe": true, "suggest": true,
	"suggested": true, "suggests": true, "uncertain": true, "unhealthy": true, "unsafe": true,
	"urgent": true, "urgently": true, "warning": true, "warn": true, "worse": true,
}

func containsNumber(source, wanted string) bool {
	target, ok := numberValue(wanted)
	if !ok {
		return false
	}
	for _, candidate := range coachingNumber.FindAllString(source, -1) {
		value, ok := numberValue(candidate)
		if ok && value.Cmp(target) == 0 {
			return true
		}
	}
	return false
}

func numberValue(value string) (*big.Rat, bool) {
	parsed, ok := new(big.Rat).SetString(strings.ReplaceAll(value, ",", ""))
	return parsed, ok
}

// PublishErrorCode is safe to log without exposing model wording or records.
func PublishErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrStale):
		return "context_changed"
	case errors.Is(err, ErrUnsupported):
		return "output_not_grounded"
	case errors.Is(err, ErrInvalid):
		return "output_invalid"
	default:
		return "storage_failed"
	}
}

func sameContext(a, b Context) bool {
	return a.HouseholdID == b.HouseholdID && a.Scope == b.Scope && a.SharedRevision == b.SharedRevision && a.PersonalRevision == b.PersonalRevision && a.SourceFingerprint == b.SourceFingerprint
}
func evidenceStillVisible(cached, current []Fact) bool {
	allowed := map[string]struct{}{}
	for _, f := range current {
		allowed[f.EvidenceID] = struct{}{}
	}
	for _, f := range cached {
		if _, ok := allowed[f.EvidenceID]; !ok {
			return false
		}
	}
	return true
}
func evidenceID(household string, f Fact) string {
	sum := sha256.Sum256([]byte(household + "\x00" + f.Family + "\x00" + f.RecordID + "\x00" + f.SourceID + "\x00" + string(f.Visibility)))
	return hex.EncodeToString(sum[:16])
}
func sourceFingerprint(facts []Fact) string {
	parts := make([]string, 0, len(facts))
	for _, f := range facts {
		parts = append(parts, f.SourceID+"\x00"+f.EvidenceID)
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}
func containsEvidence(items []Item, id string) bool {
	for _, item := range items {
		for _, candidate := range item.EvidenceIDs {
			if candidate == id {
				return true
			}
		}
	}
	return false
}
func day(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }

func revisions(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, actor policy.ActorScope) (int64, int64, error) {
	var shared, personal int64
	err := q.QueryRowContext(ctx, `SELECT hr.shared_revision,ur.personal_revision FROM household_revisions hr JOIN user_revisions ur ON ur.household_id=hr.household_id JOIN household_members m ON m.household_id=hr.household_id AND m.user_id=ur.user_id JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id WHERE hr.household_id=? AND ur.user_id=? AND u.status='active' AND h.status='active'`, actor.HouseholdID, actor.ActorID).Scan(&shared, &personal)
	return shared, personal, err
}
func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// EnsureNudge creates at most one in-app nudge for one visible evidence item.
// Follow-up remains off unless the user explicitly enables it.
func (s *Service) EnsureNudge(ctx context.Context, actor policy.ActorScope, family, recordID, sourceID string) (Nudge, error) {
	if family != "finance" && family != "health" && family != "planning" || recordID == "" || sourceID == "" {
		return Nudge{}, ErrInvalid
	}
	personal, err := s.BuildContext(ctx, actor, policy.Personal)
	if err != nil {
		return Nudge{}, err
	}
	shared, err := s.BuildContext(ctx, actor, policy.Shared)
	if err != nil {
		return Nudge{}, err
	}
	visible := false
	for _, c := range []Context{personal, shared} {
		facts := c.ReviewFacts
		if facts == nil {
			facts = c.Facts
		}
		for _, f := range facts {
			if f.Family == family && f.RecordID == recordID && f.SourceID == sourceID {
				visible = true
			}
		}
	}
	if !visible {
		return Nudge{}, policy.ErrUnauthorized
	}
	id, err := randomID()
	if err != nil {
		return Nudge{}, err
	}
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `INSERT INTO coaching_nudges(id,household_id,owner_user_id,record_family,record_id,source_id,state,created_at,updated_at) VALUES(?,?,?,?,?,?,'awaiting-update',?,?) ON CONFLICT(household_id,owner_user_id,record_family,record_id) DO NOTHING`, id, actor.HouseholdID, actor.ActorID, family, recordID, sourceID, stamp, stamp)
	if err != nil {
		return Nudge{}, err
	}
	return s.Nudge(ctx, actor, family, recordID)
}

func (s *Service) Nudge(ctx context.Context, actor policy.ActorScope, family, recordID string) (Nudge, error) {
	var n Nudge
	var follow, initial, followSent int
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT n.id,n.record_family,n.record_id,n.source_id,n.state,n.follow_up_enabled,n.initial_email_sent_at IS NOT NULL,n.follow_up_email_sent_at IS NOT NULL,n.created_at FROM coaching_nudges n JOIN household_members m ON m.household_id=n.household_id AND m.user_id=n.owner_user_id JOIN users u ON u.id=m.user_id AND u.status='active' JOIN households h ON h.id=m.household_id AND h.status='active' JOIN sources s ON s.id=n.source_id AND s.state='live' WHERE n.household_id=? AND n.owner_user_id=? AND n.record_family=? AND n.record_id=?`, actor.HouseholdID, actor.ActorID, family, recordID).Scan(&n.ID, &n.Family, &n.RecordID, &n.SourceID, &n.State, &follow, &initial, &followSent, &created)
	if err != nil {
		return Nudge{}, ErrNudgeMissing
	}
	n.FollowUpEnabled = follow == 1
	n.InitialEmailSent = initial == 1
	n.FollowUpEmailSent = followSent == 1
	n.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return n, nil
}

func (s *Service) ListNudges(ctx context.Context, actor policy.ActorScope) ([]Nudge, error) {
	if !actor.Valid() {
		return nil, policy.ErrUnauthorized
	}
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `UPDATE coaching_nudges SET state='stale',updated_at=? WHERE household_id=? AND owner_user_id=? AND state='awaiting-update' AND (NOT EXISTS (SELECT 1 FROM sources s WHERE s.id=coaching_nudges.source_id AND s.state='live') OR NOT EXISTS (SELECT 1 FROM search_entries se WHERE se.household_id=coaching_nudges.household_id AND se.record_family=coaching_nudges.record_family AND se.record_id=coaching_nudges.record_id AND (se.visibility='shared' OR (se.visibility='personal' AND se.owner_user_id=coaching_nudges.owner_user_id))))`, stamp, actor.HouseholdID, actor.ActorID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT n.id,n.record_family,n.record_id,n.source_id,n.state,n.follow_up_enabled,n.initial_email_sent_at IS NOT NULL,n.follow_up_email_sent_at IS NOT NULL,n.created_at FROM coaching_nudges n JOIN household_members m ON m.household_id=n.household_id AND m.user_id=n.owner_user_id JOIN users u ON u.id=m.user_id AND u.status='active' JOIN households h ON h.id=m.household_id AND h.status='active' JOIN sources s ON s.id=n.source_id AND s.state='live' JOIN search_entries se ON se.household_id=n.household_id AND se.record_family=n.record_family AND se.record_id=n.record_id AND (se.visibility='shared' OR (se.visibility='personal' AND se.owner_user_id=n.owner_user_id)) WHERE n.household_id=? AND n.owner_user_id=? AND n.state='awaiting-update' ORDER BY n.created_at LIMIT 3`, actor.HouseholdID, actor.ActorID)
	if err != nil {
		return nil, err
	}
	var out []Nudge
	for rows.Next() {
		var n Nudge
		var follow, initial, followSent int
		var created string
		if err := rows.Scan(&n.ID, &n.Family, &n.RecordID, &n.SourceID, &n.State, &follow, &initial, &followSent, &created); err != nil {
			return nil, err
		}
		n.FollowUpEnabled = follow == 1
		n.InitialEmailSent = initial == 1
		n.FollowUpEmailSent = followSent == 1
		n.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	active := out[:0]
	for _, n := range out {
		ok, err := activeNudgeRecord(ctx, s.db, n.Family, n.RecordID)
		if err != nil {
			return nil, err
		}
		if !ok {
			if _, err := s.db.ExecContext(ctx, `UPDATE coaching_nudges SET state='stale',updated_at=? WHERE id=? AND state='awaiting-update'`, stamp, n.ID); err != nil {
				return nil, err
			}
			continue
		}
		active = append(active, n)
	}
	return active, nil
}

func activeNudgeRecord(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, family, id string) (bool, error) {
	var query string
	switch family {
	case "finance":
		query = `SELECT EXISTS(SELECT 1 FROM finance_income WHERE id=? AND active=1 UNION ALL SELECT 1 FROM finance_spending WHERE id=? AND active=1 UNION ALL SELECT 1 FROM finance_assets WHERE id=? AND active=1 UNION ALL SELECT 1 FROM finance_liabilities WHERE id=? AND active=1 UNION ALL SELECT 1 FROM finance_budgets WHERE id=? AND active=1 UNION ALL SELECT 1 FROM finance_obligations WHERE id=? AND active=1)`
	case "health":
		query = `SELECT EXISTS(SELECT 1 FROM health_observations WHERE id=? AND active=1 UNION ALL SELECT 1 FROM health_appointments WHERE id=? AND active=1 UNION ALL SELECT 1 FROM health_care_routines WHERE id=? AND active=1)`
	case "planning":
		query = `SELECT EXISTS(SELECT 1 FROM planning_goals WHERE id=? AND active=1 UNION ALL SELECT 1 FROM planning_plans WHERE id=? AND active=1 UNION ALL SELECT 1 FROM planning_milestones WHERE id=? AND active=1 UNION ALL SELECT 1 FROM planning_events WHERE id=? AND active=1)`
	default:
		return false, nil
	}
	count := strings.Count(query, "?")
	args := make([]any, count)
	for i := range args {
		args[i] = id
	}
	var exists int
	err := q.QueryRowContext(ctx, query, args...).Scan(&exists)
	return exists == 1, err
}

func (s *Service) UpdateNudge(ctx context.Context, actor policy.ActorScope, id, action string) error {
	var set string
	switch action {
	case "acknowledge":
		set = "state='acknowledged',acknowledged_at=?"
	case "awaiting-update":
		set = "state='awaiting-update'"
	case "complete":
		set = "state='completed'"
	case "enable-follow-up":
		set = "follow_up_enabled=1"
	case "disable-follow-up":
		set = "follow_up_enabled=0"
	case "initial-email-sent":
		set = "initial_email_sent_at=COALESCE(initial_email_sent_at,?)"
	case "follow-up-email-sent":
		set = "follow_up_email_sent_at=CASE WHEN follow_up_enabled=1 THEN COALESCE(follow_up_email_sent_at,?) ELSE follow_up_email_sent_at END"
	default:
		return ErrInvalid
	}
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	args := []any{stamp}
	if !strings.Contains(set, "?") {
		args = nil
	}
	query := `UPDATE coaching_nudges SET ` + set + `,updated_at=? WHERE id=? AND household_id=? AND owner_user_id=? AND state NOT IN ('stale','completed')`
	args = append(args, stamp, id, actor.HouseholdID, actor.ActorID)
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrNudgeMissing
	}
	return nil
}
