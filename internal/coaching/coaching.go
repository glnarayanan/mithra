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
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/glnarayanan/mithra/internal/policy"
)

const (
	PromptVersion = "coaching-v5"
	SchemaVersion = "coaching-v3"
)

var (
	ErrInvalid      = errors.New("coaching input is invalid")
	ErrStale        = errors.New("coaching context changed")
	ErrUnsupported  = errors.New("coaching output is unsupported by evidence")
	ErrNudgeMissing = errors.New("nudge is unavailable")
)

type Fact struct {
	EvidenceID string            `json:"evidence_id"`
	Family     string            `json:"family"`
	RecordID   string            `json:"-"`
	Content    string            `json:"content"`
	Date       string            `json:"date,omitempty"`
	Issue      string            `json:"issue,omitempty"`
	SourceID   string            `json:"-"`
	Visibility policy.Visibility `json:"visibility"`
	CreatedAt  time.Time         `json:"created_at"`
}

type Context struct {
	HouseholdID       string   `json:"household_id"`
	Scope             string   `json:"scope"`
	SharedRevision    int64    `json:"shared_revision"`
	PersonalRevision  int64    `json:"personal_revision"`
	SourceFingerprint string   `json:"source_fingerprint"`
	Facts             []Fact   `json:"facts"`
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
	evidence, source   string
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
	signals, err := querySignals(ctx, tx, actor, visibility, facts, s.now().UTC())
	if err != nil {
		return Context{}, err
	}
	fingerprint := sourceFingerprint(facts)
	personalKey := personal
	if visibility == policy.Shared {
		personalKey = 0
	}
	return Context{HouseholdID: actor.HouseholdID, Scope: string(visibility), SharedRevision: shared, PersonalRevision: personalKey, SourceFingerprint: fingerprint, Facts: facts, Signals: signals}, nil
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
	result := Overview{Shared: deterministic(shared.Facts, asOf, shared.Signals...), Personal: deterministic(personal.Facts, asOf, personal.Signals...), SharedContext: shared, PersonalContext: personal, HasRecords: len(shared.Facts)+len(personal.Facts) > 0}
	if cached, state, err := s.load(ctx, actor, "brief", policy.Shared, shared); err == nil && state.Found {
		if state.Stale {
			cached.Dates = result.Shared.Dates
			cached.Inconsistencies = result.Shared.Inconsistencies
			cached.Priorities = result.Shared.Priorities
		}
		cached = withSignals(cached, shared.Signals, false)
		result.Shared, result.SharedCache = cached, state
	} else {
		result.SharedCache = state
	}
	if cached, state, err := s.load(ctx, actor, "brief", policy.Personal, personal); err == nil && state.Found {
		if state.Stale {
			cached.Dates = result.Personal.Dates
			cached.Inconsistencies = result.Personal.Inconsistencies
			cached.Priorities = result.Personal.Priorities
		}
		cached = withSignals(cached, personal.Signals, false)
		result.Personal, result.PersonalCache = cached, state
	} else {
		result.PersonalCache = state
	}
	result.SharedHistory, _ = s.history(ctx, actor, "brief", policy.Shared, shared)
	result.PersonalHistory, _ = s.history(ctx, actor, "brief", policy.Personal, personal)
	return result, nil
}

func (s *Service) Week(ctx context.Context, actor policy.ActorScope, asOf time.Time) (Overview, error) {
	shared, err := s.BuildContext(ctx, actor, policy.Shared)
	if err != nil {
		return Overview{}, err
	}
	personal, err := s.BuildContext(ctx, actor, policy.Personal)
	if err != nil {
		return Overview{}, err
	}
	result := Overview{Shared: deterministic(shared.Facts, asOf, shared.Signals...), Personal: deterministic(personal.Facts, asOf, personal.Signals...), SharedContext: shared, PersonalContext: personal, HasRecords: len(shared.Facts)+len(personal.Facts) > 0}
	if cached, state, err := s.load(ctx, actor, "week", policy.Shared, shared); err == nil && state.Found {
		if state.Stale {
			cached.Dates = result.Shared.Dates
			cached.Inconsistencies = result.Shared.Inconsistencies
			cached.Priorities = result.Shared.Priorities
		}
		cached = withSignals(cached, shared.Signals, true)
		result.Shared, result.SharedCache = cached, state
	} else {
		result.SharedCache = state
	}
	if cached, state, err := s.load(ctx, actor, "week", policy.Personal, personal); err == nil && state.Found {
		if state.Stale {
			cached.Dates = result.Personal.Dates
			cached.Inconsistencies = result.Personal.Inconsistencies
			cached.Priorities = result.Personal.Priorities
		}
		cached = withSignals(cached, personal.Signals, true)
		result.Personal, result.PersonalCache = cached, state
	} else {
		result.PersonalCache = state
	}
	result.SharedHistory, _ = s.history(ctx, actor, "week", policy.Shared, shared)
	result.PersonalHistory, _ = s.history(ctx, actor, "week", policy.Personal, personal)
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

func (s *Service) load(ctx context.Context, actor policy.ActorScope, mode string, visibility policy.Visibility, current Context) (Narrative, CacheState, error) {
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
	state.Stale = shared != current.SharedRevision || personal != current.PersonalRevision
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
		ownerClause = " AND se.owner_user_id=?"
		args = append(args, actor.ActorID)
	}
	rows, err := tx.QueryContext(ctx, `SELECT se.record_family,se.record_id,se.content,se.visibility,el.source_id,el.created_at,
	COALESCE(
	 (SELECT received_on FROM finance_income WHERE id=se.record_id AND active=1),
	 (SELECT spent_on FROM finance_spending WHERE id=se.record_id AND active=1),
	 (SELECT observed_on FROM finance_assets WHERE id=se.record_id AND active=1),
	 (SELECT observed_on FROM finance_liabilities WHERE id=se.record_id AND active=1),
	 (SELECT starts_on FROM finance_budgets WHERE id=se.record_id AND active=1),
	 (SELECT due_on FROM finance_obligations WHERE id=se.record_id AND active=1),
	 (SELECT observed_on FROM health_observations WHERE id=se.record_id AND active=1),
	 (SELECT scheduled_on FROM health_appointments WHERE id=se.record_id AND active=1),
	 (SELECT next_due_on FROM health_care_routines WHERE id=se.record_id AND active=1),
	 (SELECT target_on FROM planning_goals WHERE id=se.record_id AND active=1),
	 (SELECT due_on FROM planning_milestones WHERE id=se.record_id AND active=1),
	 (SELECT COALESCE(NULLIF(starts_on,''),substr(starts_at,1,10)) FROM planning_events WHERE id=se.record_id AND active=1),''),
	COALESCE(
	 (SELECT incomplete_reason FROM finance_income WHERE id=se.record_id AND active=1),
	 (SELECT incomplete_reason FROM finance_spending WHERE id=se.record_id AND active=1),
	 (SELECT incomplete_reason FROM finance_assets WHERE id=se.record_id AND active=1),
	 (SELECT incomplete_reason FROM finance_liabilities WHERE id=se.record_id AND active=1),
	 (SELECT incomplete_reason FROM finance_budgets WHERE id=se.record_id AND active=1),
	 (SELECT incomplete_reason FROM finance_obligations WHERE id=se.record_id AND active=1),'')
	FROM search_entries se
	JOIN evidence_links el ON el.record_family=se.record_family AND el.record_id=se.record_id AND el.household_id=se.household_id AND el.visibility=se.visibility AND el.owner_user_id=se.owner_user_id
	JOIN sources src ON src.id=el.source_id AND src.state='live' AND src.household_id=se.household_id
	  AND ((se.visibility='shared' AND src.visibility='shared') OR (se.visibility='personal' AND (src.visibility='shared' OR (src.visibility='personal' AND src.owner_user_id=se.owner_user_id))))
	WHERE se.household_id=? AND se.visibility=?`+ownerClause+`
	AND (
	 (se.record_family='finance' AND EXISTS (
	  SELECT 1 FROM finance_income WHERE id=se.record_id AND active=1 UNION ALL
	  SELECT 1 FROM finance_spending WHERE id=se.record_id AND active=1 UNION ALL
	  SELECT 1 FROM finance_assets WHERE id=se.record_id AND active=1 UNION ALL
	  SELECT 1 FROM finance_liabilities WHERE id=se.record_id AND active=1 UNION ALL
	  SELECT 1 FROM finance_budgets WHERE id=se.record_id AND active=1 UNION ALL
	  SELECT 1 FROM finance_obligations WHERE id=se.record_id AND active=1))
	 OR (se.record_family='health' AND EXISTS (
	  SELECT 1 FROM health_observations WHERE id=se.record_id AND active=1 UNION ALL
	  SELECT 1 FROM health_appointments WHERE id=se.record_id AND active=1 UNION ALL
	  SELECT 1 FROM health_care_routines WHERE id=se.record_id AND active=1))
	 OR (se.record_family='planning' AND EXISTS (
	  SELECT 1 FROM planning_goals WHERE id=se.record_id AND active=1 UNION ALL
	  SELECT 1 FROM planning_plans WHERE id=se.record_id AND active=1 UNION ALL
	  SELECT 1 FROM planning_milestones WHERE id=se.record_id AND active=1 UNION ALL
	  SELECT 1 FROM planning_events WHERE id=se.record_id AND active=1))
	)
	GROUP BY se.record_family,se.record_id,se.content,se.visibility,el.source_id,el.created_at ORDER BY el.created_at,se.record_family,se.record_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Fact
	for rows.Next() {
		var f Fact
		var created, visible string
		if err := rows.Scan(&f.Family, &f.RecordID, &f.Content, &visible, &f.SourceID, &created, &f.Date, &f.Issue); err != nil {
			return nil, err
		}
		f.Visibility = policy.Visibility(visible)
		f.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
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
func querySignals(ctx context.Context, tx *sql.Tx, actor policy.ActorScope, visibility policy.Visibility, facts []Fact, asOf time.Time) ([]Signal, error) {
	byRecord := make(map[string]Fact, len(facts))
	for _, fact := range facts {
		byRecord[fact.RecordID] = fact
	}
	ownerClause, args := "", []any{actor.HouseholdID, string(visibility)}
	if visibility == policy.Personal {
		ownerClause, args = " AND owner_user_id=?", append(args, actor.ActorID)
	}
	var out []Signal

	// Finance: compare the two most recent months that have visible spending.
	rows, err := tx.QueryContext(ctx, `SELECT id,substr(spent_on,1,7),amount_coefficient,amount_scale FROM finance_spending WHERE household_id=? AND visibility=? AND active=1`+ownerClause+` AND amount_coefficient IS NOT NULL ORDER BY spent_on DESC,id`, args...)
	if err != nil {
		return nil, err
	}
	months := map[string][]amountRow{}
	for rows.Next() {
		var id, month string
		var coefficient, scale int64
		if err := rows.Scan(&id, &month, &coefficient, &scale); err != nil {
			rows.Close()
			return nil, err
		}
		if fact, ok := byRecord[id]; ok {
			months[month] = append(months[month], amountRow{coefficient, scale, fact.EvidenceID, fact.SourceID})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	latestMonth, priorMonth := asOf.Format("2006-01"), asOf.AddDate(0, -1, 0).Format("2006-01")
	if count := len(months[latestMonth]) + len(months[priorMonth]); count > 0 {
		evidence := amountEvidence(append(months[latestMonth], months[priorMonth]...), 12)
		if len(evidence) > 0 {
			out = append(out, Signal{Kind: "finance_monthly_spending", Period: priorMonth + " to " + latestMonth, Summary: "Spending recorded for " + latestMonth + " is " + sumAmounts(months[latestMonth]) + " compared with " + sumAmounts(months[priorMonth]) + " for " + priorMonth + ".", EvidenceIDs: evidence})
		}
	}

	// Health: compare the first and last values only within the same stored key
	// and unit. This is a factual series, never a clinical interpretation.
	type healthRow struct{ date, value, unit, evidence, source string }
	series := map[string][]healthRow{}
	rows, err = tx.QueryContext(ctx, `SELECT id,comparability_key,observed_on,value_original,unit FROM health_observations WHERE household_id=? AND visibility=? AND active=1`+ownerClause+` ORDER BY observed_on,id`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, key, date, value, unit string
		if err := rows.Scan(&id, &key, &date, &value, &unit); err != nil {
			rows.Close()
			return nil, err
		}
		if fact, ok := byRecord[id]; ok {
			series[key] = append(series[key], healthRow{date, value, unit, fact.EvidenceID, fact.SourceID})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	seriesKeys := make([]string, 0, len(series))
	for key := range series {
		seriesKeys = append(seriesKeys, key)
	}
	sort.Strings(seriesKeys)
	healthSignals := 0
	for _, key := range seriesKeys {
		points := series[key]
		if len(points) < 2 {
			continue
		}
		first, last := points[0], points[len(points)-1]
		evidence := completeSourceEvidence([]evidenceRef{{first.evidence, first.source}, {last.evidence, last.source}}, 12)
		if len(evidence) == 0 {
			continue
		}
		out = append(out, Signal{Kind: "health_series", Period: first.date + " to " + last.date, Summary: "A comparable health measurement changed from " + first.value + " " + first.unit + " on " + first.date + " to " + last.value + " " + last.unit + " on " + last.date + ". This is a record comparison, not health advice.", EvidenceIDs: evidence})
		healthSignals++
		if healthSignals == 2 {
			break
		}
	}

	// Planning: dates are already selected from the typed records in queryFacts.
	upcoming := make([]Fact, 0, 6)
	start, end := day(asOf), day(asOf).AddDate(0, 0, 31)
	for _, fact := range facts {
		if fact.Family != "planning" {
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
			out = append(out, Signal{Kind: "planning_upcoming", Period: start.Format("2006-01-02") + " to " + end.Format("2006-01-02"), Summary: strconv.Itoa(len(upcoming)) + " dated planning records fall in the next 31 days, from " + upcoming[0].Date + " to " + upcoming[len(upcoming)-1].Date + ".", EvidenceIDs: evidence})
		}
	}

	// Week comparison is available in both modes; Week in Review can explain it.
	currentStart, priorStart := day(asOf).AddDate(0, 0, -6), day(asOf).AddDate(0, 0, -13)
	current, prior := make([]Fact, 0, 12), make([]Fact, 0, 12)
	for _, fact := range facts {
		created := day(fact.CreatedAt)
		if !created.Before(currentStart) && !created.After(day(asOf)) {
			current = append(current, fact)
		}
		if !created.Before(priorStart) && created.Before(currentStart) {
			prior = append(prior, fact)
		}
	}
	if len(current)+len(prior) > 0 {
		evidence := completeFactEvidence(append(current, prior...), 12)
		if len(current)+len(prior) > 12 {
			evidence = representativeEvidence(current, prior, 12)
		}
		if len(evidence) == 0 {
			return out, nil
		}
		summary := strconv.Itoa(len(current)) + " visible records were added in the current seven days, compared with " + strconv.Itoa(len(prior)) + " in the prior seven days."
		if len(current)+len(prior) > 12 {
			switch {
			case len(current) > 0 && len(prior) > 0:
				summary = "Visible records were added in both the current and prior seven-day periods."
			case len(current) > 0:
				summary = "Visible records were added in the current seven-day period, with none visible in the prior period."
			default:
				summary = "Visible records were added in the prior seven-day period, with none visible in the current period."
			}
		}
		out = append(out, Signal{Kind: "weekly_activity", Period: priorStart.Format("2006-01-02") + " to " + day(asOf).Format("2006-01-02"), Summary: summary, EvidenceIDs: evidence})
	}
	return out, nil
}

func representativeEvidence(first, second []Fact, limit int) []string {
	refs := make([]evidenceRef, 0, len(first)+len(second))
	for _, group := range [][]Fact{first, second} {
		if len(group) > 0 {
			refs = append(refs, evidenceRef{group[0].EvidenceID, group[0].SourceID})
		}
	}
	for _, group := range [][]Fact{first, second} {
		if len(group) == 0 {
			continue
		}
		for _, fact := range group[1:] {
			refs = append(refs, evidenceRef{fact.EvidenceID, fact.SourceID})
		}
	}
	return representativeSourceEvidence(refs, limit)
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

func representativeSourceEvidence(refs []evidenceRef, limit int) []string {
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
	total := new(big.Int)
	for _, row := range rows {
		value := big.NewInt(row.coefficient)
		if row.scale < 6 {
			value.Mul(value, new(big.Int).Exp(big.NewInt(10), big.NewInt(6-row.scale), nil))
		}
		total.Add(total, value)
	}
	negative := total.Sign() < 0
	if negative {
		total.Abs(total)
	}
	raw := total.Text(10)
	for len(raw) < 7 {
		raw = "0" + raw
	}
	whole, fraction := raw[:len(raw)-6], strings.TrimRight(raw[len(raw)-6:], "0")
	if fraction != "" {
		whole += "." + fraction
	}
	if negative {
		return "-" + whole
	}
	return whole
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
	rows, err := tx.QueryContext(ctx, `SELECT a.id,b.id FROM health_observations a JOIN health_observations b ON b.household_id=a.household_id AND b.id>a.id AND b.subject=a.subject AND b.analyte=a.analyte AND b.unit<>a.unit WHERE a.household_id=? AND a.active=1 AND b.active=1`, householdID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var first, second string
		if err := rows.Scan(&first, &second); err != nil {
			rows.Close()
			return err
		}
		mark("health", first, second, "reported units differ")
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `SELECT DISTINCT a.id,b.id FROM planning_events a JOIN planning_events b ON b.household_id=a.household_id AND b.id>a.id JOIN planning_event_owners ao ON ao.event_id=a.id JOIN planning_event_owners bo ON bo.event_id=b.id AND bo.user_id=ao.user_id WHERE a.household_id=? AND a.active=1 AND b.active=1 AND a.status='planned' AND b.status='planned' AND COALESCE(NULLIF(a.starts_on,''),a.starts_at)<COALESCE(date(NULLIF(b.ends_on,''),'+1 day'),date(NULLIF(b.starts_on,''),'+1 day'),NULLIF(b.ends_at,'')) AND COALESCE(NULLIF(b.starts_on,''),b.starts_at)<COALESCE(date(NULLIF(a.ends_on,''),'+1 day'),date(NULLIF(a.starts_on,''),'+1 day'),NULLIF(a.ends_at,''))`, householdID)
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

func joinIssue(current, next string) string {
	if current == "" {
		return next
	}
	if strings.Contains(current, next) {
		return current
	}
	return current + "; " + next
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
		if signal.Kind == "weekly_activity" && len(out.Changes) < 6 {
			out.Changes = append([]Item{item}, out.Changes...)
		}
	}
	return out
}

func signalTitle(kind string) string {
	switch kind {
	case "finance_monthly_spending":
		return "Spending comparison"
	case "health_series":
		return "Health record comparison"
	case "planning_upcoming":
		return "Plans in the next month"
	case "weekly_activity":
		return "This week and last week"
	default:
		return "Recorded pattern"
	}
}

func withSignals(n Narrative, signals []Signal, week bool) Narrative {
	for _, signal := range signals {
		item := Item{Title: signalTitle(signal.Kind), Copy: signal.Summary, When: signal.Period, EvidenceIDs: signal.EvidenceIDs}
		if len(n.Insights) < 5 && !hasEvidence(n.Insights, item.EvidenceIDs) {
			n.Insights = append(n.Insights, item)
		}
		if week && signal.Kind == "weekly_activity" && !hasEvidence(n.Changes, item.EvidenceIDs) {
			n.Changes = append([]Item{item}, n.Changes...)
			if len(n.Changes) > 12 {
				n.Changes = n.Changes[:12]
			}
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
	valid := false
	if !emptyItem(n.Lead) && validateItem(n.Lead, allowed, signals...) == nil {
		out.Lead = n.Lead
		valid = true
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
			valid = true
		}
		return out
	}
	out.Insights = keep(n.Insights, true)
	out.Changes = keep(n.Changes, false)
	out.Dates = keep(n.Dates, false)
	out.Inconsistencies = keep(n.Inconsistencies, false)
	out.Priorities = keep(n.Priorities, false)
	if len(out.Insights) == 0 {
		return Narrative{}, ErrUnsupported
	}
	if len(signals) > 0 && !hasExactSignalInsight(out.Insights, signals) {
		return Narrative{}, ErrUnsupported
	}
	if !valid {
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
	if len(item.Title) > 256 || len(item.Copy) > 1200 || len(item.EvidenceIDs) == 0 || len(item.EvidenceIDs) > 12 || imperativeWording(item.Title) || imperativeWording(item.Copy) || unsafeWording(item.Title+" "+item.Copy, fullyCitesSignal(item.EvidenceIDs, signals)) {
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
	if !groundedWording(item.Title+" "+item.Copy+" "+item.When, cited, signals, item.EvidenceIDs) {
		return ErrUnsupported
	}
	return nil
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
		for _, f := range c.Facts {
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
