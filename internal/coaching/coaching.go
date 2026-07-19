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
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/glnarayanan/mithra/internal/policy"
)

const (
	PromptVersion = "coaching-v2"
	SchemaVersion = "coaching-v1"
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
	HouseholdID       string `json:"household_id"`
	Scope             string `json:"scope"`
	SharedRevision    int64  `json:"shared_revision"`
	PersonalRevision  int64  `json:"personal_revision"`
	SourceFingerprint string `json:"source_fingerprint"`
	Facts             []Fact `json:"facts"`
}

type Item struct {
	Title       string   `json:"title"`
	Copy        string   `json:"copy"`
	When        string   `json:"when,omitempty"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type Narrative struct {
	Lead            Item   `json:"lead"`
	Changes         []Item `json:"changes"`
	Dates           []Item `json:"dates"`
	Inconsistencies []Item `json:"inconsistencies"`
	Priorities      []Item `json:"priorities"`
}

type Overview struct {
	Shared                     Narrative
	Personal                   Narrative
	SharedContext              Context
	PersonalContext            Context
	HasRecords                 bool
	SharedCache, PersonalCache CacheState
}

type CacheState struct {
	Found, Stale bool
	GeneratedAt  time.Time
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
	shared, personal, err := revisions(ctx, tx, actor)
	if err != nil {
		return Context{}, policy.ErrUnauthorized
	}
	facts, err := queryFacts(ctx, tx, actor, visibility)
	if err != nil {
		return Context{}, err
	}
	if err := tx.Commit(); err != nil {
		return Context{}, err
	}
	fingerprint := sourceFingerprint(facts)
	personalKey := personal
	if visibility == policy.Shared {
		personalKey = 0
	}
	return Context{HouseholdID: actor.HouseholdID, Scope: string(visibility), SharedRevision: shared, PersonalRevision: personalKey, SourceFingerprint: fingerprint, Facts: facts}, nil
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
	result := Overview{Shared: deterministic(shared.Facts, asOf), Personal: deterministic(personal.Facts, asOf), SharedContext: shared, PersonalContext: personal, HasRecords: len(shared.Facts)+len(personal.Facts) > 0}
	if cached, state, err := s.load(ctx, actor, "brief", policy.Shared, shared); err == nil && state.Found {
		if state.Stale {
			cached.Dates = result.Shared.Dates
			cached.Inconsistencies = result.Shared.Inconsistencies
			cached.Priorities = result.Shared.Priorities
		}
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
		result.Personal, result.PersonalCache = cached, state
	} else {
		result.PersonalCache = state
	}
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
	result := Overview{Shared: deterministic(shared.Facts, asOf), Personal: deterministic(personal.Facts, asOf), SharedContext: shared, PersonalContext: personal, HasRecords: len(shared.Facts)+len(personal.Facts) > 0}
	if cached, state, err := s.load(ctx, actor, "week", policy.Shared, shared); err == nil && state.Found {
		if state.Stale {
			cached.Dates = result.Shared.Dates
			cached.Inconsistencies = result.Shared.Inconsistencies
			cached.Priorities = result.Shared.Priorities
		}
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
		result.Personal, result.PersonalCache = cached, state
	} else {
		result.PersonalCache = state
	}
	return result, nil
}

// Publish rebuilds permitted context immediately before storing model wording.
// Shared and personal results are validated and cached independently.
func (s *Service) Publish(ctx context.Context, actor policy.ActorScope, mode string, visibility policy.Visibility, expected Context, output Narrative, model string) error {
	if mode != "brief" && mode != "week" || strings.TrimSpace(model) == "" || len(model) > 64 {
		return ErrInvalid
	}
	current, err := s.BuildContext(ctx, actor, visibility)
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
	if err := validateNarrative(output, allowed); err != nil {
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	shared, personal, err := revisions(ctx, tx, actor)
	if err != nil {
		return policy.ErrUnauthorized
	}
	if shared != current.SharedRevision || (visibility == policy.Personal && personal != current.PersonalRevision) {
		return ErrStale
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO coaching_cache(id,household_id,owner_user_id,mode,visibility,content_json,evidence_json,shared_revision,personal_revision,source_fingerprint,model,prompt_version,schema_version,generated_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(household_id,IFNULL(owner_user_id,''),mode,visibility) DO UPDATE SET content_json=excluded.content_json,evidence_json=excluded.evidence_json,shared_revision=excluded.shared_revision,personal_revision=excluded.personal_revision,source_fingerprint=excluded.source_fingerprint,model=excluded.model,prompt_version=excluded.prompt_version,schema_version=excluded.schema_version,generated_at=excluded.generated_at,updated_at=excluded.updated_at`, id, actor.HouseholdID, owner, mode, visibility, string(content), string(evidence), current.SharedRevision, current.PersonalRevision, current.SourceFingerprint, model, PromptVersion, SchemaVersion, stamp, stamp, stamp)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) load(ctx context.Context, actor policy.ActorScope, mode string, visibility policy.Visibility, current Context) (Narrative, CacheState, error) {
	owner := actor.ActorID
	if visibility == policy.Shared {
		owner = ""
	}
	var encoded, evidence, generated, promptVersion, schemaVersion string
	var shared, personal int64
	err := s.db.QueryRowContext(ctx, `SELECT content_json,evidence_json,shared_revision,personal_revision,generated_at,prompt_version,schema_version FROM coaching_cache WHERE household_id=? AND IFNULL(owner_user_id,'')=? AND mode=? AND visibility=?`, actor.HouseholdID, owner, mode, visibility).Scan(&encoded, &evidence, &shared, &personal, &generated, &promptVersion, &schemaVersion)
	if err != nil {
		return Narrative{}, CacheState{}, err
	}
	if promptVersion != PromptVersion || schemaVersion != SchemaVersion {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM coaching_cache WHERE household_id=? AND IFNULL(owner_user_id,'')=? AND mode=? AND visibility=?`, actor.HouseholdID, owner, mode, visibility)
		return Narrative{}, CacheState{}, ErrStale
	}
	state := CacheState{Found: true}
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
		mark("health", first, second, "reported units differ; compare the source context")
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

func deterministic(facts []Fact, asOf time.Time) Narrative {
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
			issue.Copy = "The source needs a correction: " + f.Issue + "."
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
	return out
}

func factItem(f Fact) Item {
	copy := "Recorded in " + f.Family + "."
	if f.Date != "" {
		copy = "Recorded for " + f.Date + " in " + f.Family + "."
	}
	return Item{Title: f.Content, Copy: copy, When: f.Date, EvidenceIDs: []string{f.EvidenceID}}
}

func validateNarrative(n Narrative, allowed map[string]Fact) error {
	if len(n.Priorities) > 3 || len(n.Changes) > 12 || len(n.Dates) > 12 || len(n.Inconsistencies) > 12 {
		return ErrInvalid
	}
	items := []Item{n.Lead}
	items = append(items, n.Changes...)
	items = append(items, n.Dates...)
	items = append(items, n.Inconsistencies...)
	items = append(items, n.Priorities...)
	for _, item := range items {
		if strings.TrimSpace(item.Title) == "" && strings.TrimSpace(item.Copy) == "" {
			continue
		}
		if len(item.Title) > 256 || len(item.Copy) > 1200 || len(item.EvidenceIDs) == 0 || len(item.EvidenceIDs) > 6 || unsafeWording(item.Title+" "+item.Copy) {
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
		if !groundedWording(item.Title+" "+item.Copy+" "+item.When, cited) {
			return ErrUnsupported
		}
	}
	return nil
}

func unsafeWording(value string) bool {
	lower := " " + strings.ToLower(value) + " "
	for _, term := range []string{" because ", " caused ", " causes ", " leads to ", " diagnosis ", " diagnose ", " treatment ", " medication ", " should take ", " you should ", " we recommend ", " seek medical ", " consult a ", " at risk ", " normal range ", " abnormal ", " unhealthy ", " blame ", " fault ", " score ", " reset ", " increased ", " decreased ", " higher ", " lower ", " rising ", " falling ", " overdue "} {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

var coachingNumber = regexp.MustCompile(`[+-]?[0-9]+(?:[.,][0-9]+)*`)
var coachingWord = regexp.MustCompile(`[a-zA-Z]{4,}`)

func groundedWording(wording string, facts []Fact) bool {
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
	source := evidence.String()
	lower := strings.ToLower(wording)
	for _, number := range coachingNumber.FindAllString(lower, -1) {
		if !strings.Contains(source, number) {
			return false
		}
	}
	for _, word := range coachingWord.FindAllString(lower, -1) {
		if strings.Contains(source, strings.ToLower(word)) {
			return true
		}
	}
	return false
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
