package coaching

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/finance"
	"github.com/glnarayanan/mithra/internal/health"
	"github.com/glnarayanan/mithra/internal/planning"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/storage"
)

type fixture struct {
	db                       *sql.DB
	sources                  *storage.Service
	finance                  *finance.Service
	health                   *health.Service
	planning                 *planning.Service
	service                  *Service
	owner, partner, outsider policy.ActorScope
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "mithra.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	stamp := "2026-07-01T00:00:00Z"
	for _, row := range [][]string{{"owner", "owner@example.com"}, {"partner", "partner@example.com"}, {"outsider", "outsider@example.com"}} {
		if _, err := db.Exec(`INSERT INTO users(id,email,status,password_hash,created_at,updated_at) VALUES(?,?,'active','hash',?,?)`, row[0], row[1], stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range [][]string{{"home", "owner"}, {"other", "outsider"}} {
		if _, err := db.Exec(`INSERT INTO households(id,status,owner_user_id,created_at,updated_at) VALUES(?,'active',?,?,?)`, row[0], row[1], stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range [][]string{{"home", "owner", "owner"}, {"home", "partner", "adult"}, {"other", "outsider", "owner"}} {
		if _, err := db.Exec(`INSERT INTO household_members(household_id,user_id,role,created_at) VALUES(?,?,?,?)`, row[0], row[1], row[2], stamp); err != nil {
			t.Fatal(err)
		}
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	sources, err := storage.New(db, filepath.Join(t.TempDir(), "sources"), key)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{db: db, sources: sources, finance: finance.New(db), health: health.New(db), planning: planning.New(db), service: New(db), owner: policy.ActorScope{ActorID: "owner", HouseholdID: "home", Role: "owner"}, partner: policy.ActorScope{ActorID: "partner", HouseholdID: "home", Role: "adult"}, outsider: policy.ActorScope{ActorID: "outsider", HouseholdID: "other", Role: "owner"}}
}

func (f fixture) source(t *testing.T, actor policy.ActorScope, visibility policy.Visibility, marker string) storage.Source {
	t.Helper()
	source, err := f.sources.Store(context.Background(), actor, []byte(marker), storage.Metadata{Family: "text", Version: 1, Visibility: visibility, LocatorKind: "source", LocatorValue: "update"})
	if err != nil {
		t.Fatal(err)
	}
	return source
}
func financeProvenance(source storage.Source) finance.Provenance {
	return finance.Provenance{SourceID: source.ID, SourceFamily: source.Family, SourceVersion: source.Version, LocatorKind: "source", LocatorValue: "update", GeneratedBy: "application", SchemaVersion: "finance-v1"}
}
func healthProvenance(source storage.Source) health.Provenance {
	return health.Provenance{SourceID: source.ID, SourceFamily: source.Family, SourceVersion: source.Version, LocatorKind: "source", LocatorValue: "update", GeneratedBy: "application", SchemaVersion: "health-v1"}
}
func planningProvenance(source storage.Source) planning.Provenance {
	return planning.Provenance{SourceID: source.ID, SourceFamily: source.Family, SourceVersion: source.Version, LocatorKind: "source", LocatorValue: "update", GeneratedBy: "application", SchemaVersion: "planning-v1"}
}

func narrativeWithTestInsight(n Narrative, facts []Fact, signals []Signal) Narrative {
	if len(signals) > 0 {
		signal := signals[0]
		n.Insights = []Item{{Title: signalTitle(signal.Kind), Copy: signal.Summary, When: signal.Period, EvidenceIDs: signal.EvidenceIDs}}
	} else if len(facts) > 0 {
		n.Insights = []Item{factItem(facts[0])}
	}
	return n
}

func TestContextSeparatesSharedPersonalPartnerAndHousehold(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	sharedSource := f.source(t, f.owner, policy.Shared, "shared-income")
	if _, err := f.finance.Create(ctx, f.owner, finance.Draft{Kind: finance.Income, Visibility: policy.Shared, Label: "Salary", Category: "Income", Date: "2026-07-17", AmountText: "5000", Provenance: financeProvenance(sharedSource)}); err != nil {
		t.Fatal(err)
	}
	ownerSource := f.source(t, f.owner, policy.Personal, "owner-health")
	if _, err := f.health.CreateObservation(ctx, f.owner, health.ObservationDraft{Visibility: policy.Personal, Subject: "Owner", Analyte: "HbA1c", ObservedOn: "2026-07-16", Value: "5.8", Unit: "%", Provenance: healthProvenance(ownerSource)}); err != nil {
		t.Fatal(err)
	}
	partnerSource := f.source(t, f.partner, policy.Personal, "partner-secret")
	if _, err := f.planning.CreateEvent(ctx, f.partner, planning.EventDraft{Visibility: policy.Personal, Title: "Partner private appointment", AllDay: true, StartsOn: "2026-07-20", Status: "planned", Provenance: planningProvenance(partnerSource)}); err != nil {
		t.Fatal(err)
	}
	outsideSource := f.source(t, f.outsider, policy.Shared, "outside")
	if _, err := f.finance.Create(ctx, f.outsider, finance.Draft{Kind: finance.Asset, Visibility: policy.Shared, Label: "Other home", Category: "Asset", Date: "2026-07-17", AmountText: "2", Provenance: financeProvenance(outsideSource)}); err != nil {
		t.Fatal(err)
	}
	shared, err := f.service.BuildContext(ctx, f.owner, policy.Shared)
	if err != nil {
		t.Fatal(err)
	}
	personal, err := f.service.BuildContext(ctx, f.owner, policy.Personal)
	if err != nil {
		t.Fatal(err)
	}
	partnerShared, err := f.service.BuildContext(ctx, f.partner, policy.Shared)
	if err != nil {
		t.Fatal(err)
	}
	if len(shared.Facts) != 1 || shared.Facts[0].Content == "" || strings.Contains(shared.Facts[0].Content, "Partner") || strings.Contains(shared.Facts[0].Content, "Other home") {
		t.Fatalf("shared facts = %#v", shared.Facts)
	}
	if len(personal.Facts) != 1 || !strings.Contains(personal.Facts[0].Content, "HbA1c") {
		t.Fatalf("personal facts = %#v", personal.Facts)
	}
	if shared.SourceFingerprint != partnerShared.SourceFingerprint || shared.Facts[0].EvidenceID != partnerShared.Facts[0].EvidenceID {
		t.Fatal("partner-private state changed shared context identity")
	}
	if shared.PersonalRevision != 0 {
		t.Fatalf("shared cache key contains personal revision: %d", shared.PersonalRevision)
	}
}

func TestDeterministicOverviewCapsPrioritiesAndRejectsUnsafeUnsupportedOutput(t *testing.T) {
	asOf := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	facts := []Fact{}
	for i := 0; i < 6; i++ {
		facts = append(facts, Fact{EvidenceID: string(rune('a' + i)), Family: "finance", Content: "Record", Date: "2026-07-20", Issue: "amount needs correction", CreatedAt: asOf})
	}
	n := deterministic(facts, asOf)
	if len(n.Priorities) != 3 {
		t.Fatalf("priorities=%d", len(n.Priorities))
	}
	allowed := map[string]Fact{"a": facts[0]}
	bad := Narrative{Lead: Item{Title: "Health", Copy: "This caused an abnormal diagnosis.", EvidenceIDs: []string{"a"}}}
	if !errors.Is(validateNarrative(bad, allowed), ErrUnsupported) {
		t.Fatal("unsafe medical or causal wording accepted")
	}
	bad.Lead.Copy = "A factual update."
	bad.Lead.EvidenceIDs = []string{"missing"}
	if !errors.Is(validateNarrative(bad, allowed), ErrUnsupported) {
		t.Fatal("unsupported evidence accepted")
	}
	bad.Lead = Item{Title: "Record increased", Copy: "Record is now 999.", EvidenceIDs: []string{"a"}}
	if !errors.Is(validateNarrative(bad, allowed), ErrUnsupported) {
		t.Fatal("invented trend or number accepted")
	}
}

func TestSignalsUseVisibleTypedRecordsAndTrustOnlyFullyCitedNumbers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.service.now = func() time.Time { return time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC) }
	for _, row := range []struct{ date, amount string }{{"2026-06-20", "10"}, {"2026-07-20", "20"}} {
		source := f.source(t, f.owner, policy.Shared, row.date)
		if _, err := f.finance.Create(ctx, f.owner, finance.Draft{Kind: finance.Spending, Visibility: policy.Shared, Label: "Food", Category: "Food", Date: row.date, AmountText: row.amount, Provenance: financeProvenance(source)}); err != nil {
			t.Fatal(err)
		}
	}
	for index, row := range []struct{ date, value string }{{"2026-06-20", "5.1"}, {"2026-07-20", "5.4"}} {
		source := f.source(t, f.owner, policy.Shared, row.date+"-health")
		if _, err := f.health.CreateObservation(ctx, f.owner, health.ObservationDraft{Visibility: policy.Shared, Subject: "Owner", Analyte: "Reading", ObservedOn: row.date, Value: row.value, Unit: "u", Provenance: healthProvenance(source)}); err != nil {
			t.Fatal(err)
		}
		_ = index
	}
	source := f.source(t, f.owner, policy.Shared, "plan")
	if _, err := f.planning.CreateEvent(ctx, f.owner, planning.EventDraft{Visibility: policy.Shared, Title: "Trip", AllDay: true, StartsOn: "2026-07-24", Status: "planned", Provenance: planningProvenance(source)}); err != nil {
		t.Fatal(err)
	}
	input, err := f.service.BuildContext(ctx, f.owner, policy.Shared)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]Signal{}
	for _, signal := range input.Signals {
		kinds[signal.Kind] = signal
	}
	for _, kind := range []string{"finance_monthly_spending", "health_series", "planning_upcoming", "weekly_activity"} {
		if len(kinds[kind].EvidenceIDs) == 0 {
			t.Fatalf("missing %s signal: %#v", kind, input.Signals)
		}
	}
	week := withSignals(Narrative{}, []Signal{kinds["weekly_activity"]}, true)
	if len(week.Changes) != 1 || week.Changes[0].Title != "This week and last week" {
		t.Fatalf("week comparison = %#v", week)
	}
	financeSignal := kinds["finance_monthly_spending"]
	if !strings.Contains(financeSignal.Summary, "20") || !strings.Contains(financeSignal.Summary, "10") {
		t.Fatalf("finance signal = %#v", financeSignal)
	}
	fact := Fact{EvidenceID: "a", Content: "Rent", Date: "2026-07-20"}
	item := Item{Title: "Rent", Copy: "Recorded 999 this month.", EvidenceIDs: []string{"a"}}
	if err := validateNarrative(Narrative{Lead: item}, map[string]Fact{"a": fact}, Signal{Summary: "Recorded 999 this month.", EvidenceIDs: []string{"a"}}); err != nil {
		t.Fatalf("trusted signal number rejected: %v", err)
	}
	if !errors.Is(validateNarrative(Narrative{Lead: item}, map[string]Fact{"a": fact}, Signal{Summary: "Recorded 999 this month.", EvidenceIDs: []string{"a", "b"}}), ErrUnsupported) {
		t.Fatal("partially cited signal number accepted")
	}
	comparison := Item{Title: "Spending increased", Copy: "Recorded spending increased from 10 to 20.", EvidenceIDs: financeSignal.EvidenceIDs}
	allowed := make(map[string]Fact, len(input.Facts))
	for _, fact := range input.Facts {
		allowed[fact.EvidenceID] = fact
	}
	if err := validateNarrative(Narrative{Lead: comparison}, allowed, financeSignal); err != nil {
		t.Fatalf("factual signal comparison rejected: %v", err)
	}
	insight := Item{Title: "Spending comparison", Copy: financeSignal.Summary, When: financeSignal.Period, EvidenceIDs: financeSignal.EvidenceIDs}
	natural := Narrative{Lead: Item{Title: "July spending rose", Copy: "Recorded spending rose from 10 to 20.0 between June and July.", EvidenceIDs: financeSignal.EvidenceIDs}, Insights: []Item{insight}}
	if err := f.service.Publish(ctx, f.owner, "brief", policy.Shared, input, natural, "test-model"); err != nil {
		t.Fatalf("natural, cited signal wording did not publish: %v", err)
	}
	if err := f.service.Publish(ctx, f.owner, "week", policy.Shared, input, Narrative{Lead: natural.Lead}, "test-model"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("lead-only narrative published: %v", err)
	}
	if len(financeSignal.EvidenceIDs) < 2 {
		t.Fatalf("finance evidence is too short for normalization test: %#v", financeSignal)
	}
	reordered := []string{financeSignal.EvidenceIDs[1], financeSignal.EvidenceIDs[0]}
	normalized := Narrative{Lead: natural.Lead, Insights: []Item{{Title: "Spending comparison", Copy: financeSignal.Summary, When: financeSignal.Period, EvidenceIDs: reordered}}}
	if err := f.service.Publish(ctx, f.owner, "week", policy.Shared, input, normalized, "test-model"); err != nil {
		t.Fatalf("reordered signal evidence did not normalize: %v", err)
	}
	var normalizedJSON string
	if err := f.db.QueryRow(`SELECT content_json FROM coaching_cache WHERE household_id='home' AND mode='week' AND visibility='shared'`).Scan(&normalizedJSON); err != nil {
		t.Fatal(err)
	}
	var normalizedStored Narrative
	if err := json.Unmarshal([]byte(normalizedJSON), &normalizedStored); err != nil || len(normalizedStored.Insights) != 1 || strings.Join(normalizedStored.Insights[0].EvidenceIDs, ",") != strings.Join(financeSignal.EvidenceIDs, ",") {
		t.Fatalf("normalized stored insight=%#v err=%v", normalizedStored, err)
	}
	unknown := normalized
	unknown.Insights[0].EvidenceIDs = []string{"unknown"}
	if err := f.service.Publish(ctx, f.owner, "week", policy.Shared, input, unknown, "test-model"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown signal evidence published: %v", err)
	}
	differentCopy := normalized
	differentCopy.Insights[0].Copy += " extra"
	if err := f.service.Publish(ctx, f.owner, "week", policy.Shared, input, differentCopy, "test-model"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("changed signal summary published: %v", err)
	}
	sharedView := Narrative{Lead: Item{Title: "Monthly spending pattern", Copy: "Recent activity listed spending from 10 to 20 against prior records.", EvidenceIDs: financeSignal.EvidenceIDs}}
	if err := validateNarrative(sharedView, allowed, financeSignal); err != nil {
		t.Fatalf("realistic shared record-view wording rejected: %v", err)
	}
	var published int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM coaching_cache WHERE household_id='home' AND mode='brief' AND visibility='shared'`).Scan(&published); err != nil || published != 1 {
		t.Fatalf("published cache count=%d err=%v", published, err)
	}
	unsupportedComparison := Item{Title: "Rent increased", Copy: "Rent is higher.", EvidenceIDs: []string{"a"}}
	if !errors.Is(validateNarrative(Narrative{Lead: unsupportedComparison}, map[string]Fact{"a": fact}), ErrUnsupported) {
		t.Fatal("comparison without a fully cited signal accepted")
	}
	fabricated := Item{Title: "Emergency", Copy: "Mithra says an emergency is coming.", EvidenceIDs: []string{"a"}}
	if !errors.Is(validateNarrative(Narrative{Lead: fabricated}, map[string]Fact{"a": fact}), ErrUnsupported) {
		t.Fatal("fabricated nonnumeric claim with a valid citation accepted")
	}
}

func TestDenseWeeklySignalUsesRepresentativeEvidenceWithoutCounts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	asOf := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	facts := make([]Fact, 0, 15)
	for i := 0; i < 8; i++ {
		facts = append(facts, Fact{EvidenceID: fmt.Sprintf("current-%d", i), RecordID: fmt.Sprintf("current-record-%d", i), Family: "finance", Content: "Current record", CreatedAt: asOf.AddDate(0, 0, -i)})
	}
	for i := 0; i < 7; i++ {
		facts = append(facts, Fact{EvidenceID: fmt.Sprintf("prior-%d", i), RecordID: fmt.Sprintf("prior-record-%d", i), Family: "finance", Content: "Prior record", CreatedAt: asOf.AddDate(0, 0, -7-i)})
	}
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	signals, err := querySignals(ctx, tx, f.owner, policy.Shared, facts, asOf)
	if err != nil {
		t.Fatal(err)
	}
	var week Signal
	for _, signal := range signals {
		if signal.Kind == "weekly_activity" {
			week = signal
		}
	}
	if week.Summary != "Visible records were added in both the current and prior seven-day periods." || len(week.EvidenceIDs) == 0 || len(week.EvidenceIDs) > 12 {
		t.Fatalf("dense week signal = %#v", week)
	}
	current, prior := false, false
	for _, id := range week.EvidenceIDs {
		current = current || strings.HasPrefix(id, "current-")
		prior = prior || strings.HasPrefix(id, "prior-")
	}
	if !current || !prior {
		t.Fatalf("dense week evidence must represent both periods: %#v", week.EvidenceIDs)
	}
	weekNarrative := withSignals(Narrative{}, []Signal{week}, true)
	if len(weekNarrative.Changes) != 1 || weekNarrative.Changes[0].Copy != week.Summary {
		t.Fatalf("week narrative = %#v", weekNarrative)
	}
}

func TestFinanceSignalUsesOneEvidenceIDPerSourceWithoutChangingTotals(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.service.now = func() time.Time { return time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC) }
	first := f.source(t, f.owner, policy.Shared, "first-import")
	second := f.source(t, f.owner, policy.Shared, "second-import")
	for _, row := range []struct {
		source storage.Source
		date   string
		amount string
	}{
		{first, "2026-06-20", "5"},
		{first, "2026-07-10", "10"},
		{first, "2026-07-20", "20"},
		{second, "2026-07-18", "7"},
	} {
		if _, err := f.finance.Create(ctx, f.owner, finance.Draft{Kind: finance.Spending, Visibility: policy.Shared, Label: "Food", Category: "Food", Date: row.date, AmountText: row.amount, Provenance: financeProvenance(row.source)}); err != nil {
			t.Fatal(err)
		}
	}
	input, err := f.service.BuildContext(ctx, f.owner, policy.Shared)
	if err != nil {
		t.Fatal(err)
	}
	var signal Signal
	for _, candidate := range input.Signals {
		if candidate.Kind == "finance_monthly_spending" {
			signal = candidate
		}
	}
	if !strings.Contains(signal.Summary, "37") || !strings.Contains(signal.Summary, "5") || len(signal.EvidenceIDs) != 2 {
		t.Fatalf("finance signal = %#v", signal)
	}
	sourceForEvidence := make(map[string]string, len(input.Facts))
	for _, fact := range input.Facts {
		sourceForEvidence[fact.EvidenceID] = fact.SourceID
	}
	seen := map[string]bool{}
	for _, id := range signal.EvidenceIDs {
		seen[sourceForEvidence[id]] = true
	}
	if !seen[first.ID] || !seen[second.ID] {
		t.Fatalf("signal did not preserve both sources: %#v", signal.EvidenceIDs)
	}
}

func TestFinanceSignalKeepsExactTotalsForManyRowsFromOneSource(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.service.now = func() time.Time { return time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC) }
	source := f.source(t, f.owner, policy.Shared, "many-rows")
	for i := 0; i < 13; i++ {
		date, amount := "2026-07-20", "1"
		if i == 0 {
			date, amount = "2026-06-20", "5"
		}
		if _, err := f.finance.Create(ctx, f.owner, finance.Draft{Kind: finance.Spending, Visibility: policy.Shared, Label: "Food", Category: "Food", Date: date, AmountText: amount, Provenance: financeProvenance(source)}); err != nil {
			t.Fatal(err)
		}
	}
	input, err := f.service.BuildContext(ctx, f.owner, policy.Shared)
	if err != nil {
		t.Fatal(err)
	}
	for _, signal := range input.Signals {
		if signal.Kind == "finance_monthly_spending" {
			if !strings.Contains(signal.Summary, "12") || !strings.Contains(signal.Summary, "5") || len(signal.EvidenceIDs) != 1 {
				t.Fatalf("many-row finance signal = %#v", signal)
			}
			return
		}
	}
	t.Fatal("many-row finance signal missing")
}

func TestFinanceSignalOmitsIncompleteDistinctSourceEvidence(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.service.now = func() time.Time { return time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC) }
	for i := 0; i < 13; i++ {
		date, amount := "2026-07-20", "1"
		if i == 0 {
			date, amount = "2026-06-20", "5"
		}
		source := f.source(t, f.owner, policy.Shared, fmt.Sprintf("distinct-source-%d", i))
		if _, err := f.finance.Create(ctx, f.owner, finance.Draft{Kind: finance.Spending, Visibility: policy.Shared, Label: "Food", Category: "Food", Date: date, AmountText: amount, Provenance: financeProvenance(source)}); err != nil {
			t.Fatal(err)
		}
	}
	input, err := f.service.BuildContext(ctx, f.owner, policy.Shared)
	if err != nil {
		t.Fatal(err)
	}
	for _, signal := range input.Signals {
		if signal.Kind == "finance_monthly_spending" {
			t.Fatalf("finance signal with incomplete source evidence = %#v", signal)
		}
	}
}

func TestPublishErrorCodeDoesNotExposeOutput(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{ErrStale, "context_changed"},
		{ErrUnsupported, "output_not_grounded"},
		{ErrInvalid, "output_invalid"},
		{errors.New("database details"), "storage_failed"},
	} {
		if got := PublishErrorCode(test.err); got != test.want {
			t.Fatalf("PublishErrorCode(%v)=%q, want %q", test.err, got, test.want)
		}
	}
}

func TestNarrativeRejectsCommandsButAllowsFactualCheckLabels(t *testing.T) {
	allowed := map[string]Fact{"a": {EvidenceID: "a", Content: "Insurance renewal Buy gold See doctor"}}
	for _, copy := range []string{
		"Check whether the insurance renewal is due.",
		"Review the insurance renewal record.",
		"Consider the insurance renewal.",
		"Remember the insurance renewal.",
		"Make sure the insurance renewal is recorded.",
		"Follow up on the insurance renewal.",
		"Contact the insurance provider.",
		"Call about the insurance renewal.",
		"Book an insurance renewal visit.",
		"Pay the insurance renewal.",
		"Update the insurance renewal.",
		"The insurance renewal needs action.",
		"The insurance renewal should be reviewed.",
		"Please review the insurance renewal.",
		"You need to call about the insurance renewal.",
		"The insurance renewal must be paid.",
		"The insurance renewal is required to be paid.",
		"Could you check the insurance renewal?",
		"Would you review the insurance renewal?",
		"Can you buy gold?",
		"Will you see doctor?",
		"Buy gold.",
		"See doctor.",
	} {
		item := Item{Title: "Insurance renewal", Copy: copy, EvidenceIDs: []string{"a"}}
		if !errors.Is(validateNarrative(Narrative{Lead: item}, allowed), ErrUnsupported) {
			t.Fatalf("command accepted: %q", copy)
		}
	}
	factual := Item{Title: "Insurance renewal check", Copy: "Insurance renewal is recorded.", EvidenceIDs: []string{"a"}}
	if err := validateNarrative(Narrative{Lead: factual}, allowed); err != nil {
		t.Fatalf("factual check label rejected: %v", err)
	}
	worthChecking := Item{Title: "Insurance renewal", Copy: "Insurance renewal may be worth checking.", EvidenceIDs: []string{"a"}}
	if err := validateNarrative(Narrative{Lead: worthChecking}, allowed); err != nil {
		t.Fatalf("non-command worth-checking wording rejected: %v", err)
	}
}

func TestNarrativeRejectsUnsupportedCertaintyUrgencyRiskCausationAndAdvice(t *testing.T) {
	allowed := map[string]Fact{"a": {EvidenceID: "a", Content: "Insurance renewal"}}
	for _, copy := range []string{
		"Insurance renewal is guaranteed and urgent.",
		"Insurance renewal is certain.",
		"Insurance renewal is definitely safe.",
		"Insurance renewal carries risk.",
		"Insurance renewal caused a change.",
		"Mithra recommends the insurance renewal.",
	} {
		item := Item{Title: "Insurance renewal", Copy: copy, EvidenceIDs: []string{"a"}}
		if !errors.Is(validateNarrative(Narrative{Lead: item}, allowed), ErrUnsupported) {
			t.Fatalf("unsupported claim accepted: %q", copy)
		}
	}
}

func TestPublishKeepsOnlyGroundedModelItems(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.service.now = func() time.Time { return time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC) }
	for _, row := range []struct{ date, amount string }{{"2026-06-20", "10"}, {"2026-07-20", "20"}} {
		source := f.source(t, f.owner, policy.Shared, row.date)
		if _, err := f.finance.Create(ctx, f.owner, finance.Draft{Kind: finance.Spending, Visibility: policy.Shared, Label: "Food", Category: "Food", Date: row.date, AmountText: row.amount, Provenance: financeProvenance(source)}); err != nil {
			t.Fatal(err)
		}
	}
	input, err := f.service.BuildContext(ctx, f.owner, policy.Shared)
	if err != nil {
		t.Fatal(err)
	}
	var signal Signal
	for _, candidate := range input.Signals {
		if candidate.Kind == "finance_monthly_spending" {
			signal = candidate
		}
	}
	if len(signal.EvidenceIDs) == 0 {
		t.Fatalf("missing finance signal: %#v", input.Signals)
	}
	valid := Item{Title: "Spending comparison", Copy: signal.Summary, When: signal.Period, EvidenceIDs: signal.EvidenceIDs}
	invalid := Item{Title: "Emergency", Copy: "Mithra says an emergency is coming.", EvidenceIDs: []string{input.Facts[0].EvidenceID}}
	mixed := Narrative{Lead: invalid, Insights: []Item{valid}, Priorities: []Item{invalid}}
	if err := f.service.Publish(ctx, f.owner, "brief", policy.Shared, input, mixed, "test-model"); err != nil {
		t.Fatalf("mixed model output did not publish: %v", err)
	}
	var encoded string
	if err := f.db.QueryRow(`SELECT content_json FROM coaching_cache WHERE household_id='home' AND mode='brief' AND visibility='shared'`).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var published Narrative
	if err := json.Unmarshal([]byte(encoded), &published); err != nil {
		t.Fatal(err)
	}
	expectedLead := deterministic(input.Facts, f.service.now(), input.Signals...).Lead
	if published.Lead.Title != expectedLead.Title || published.Lead.Copy != expectedLead.Copy || strings.Join(published.Lead.EvidenceIDs, ",") != strings.Join(expectedLead.EvidenceIDs, ",") || len(published.Insights) != 1 || published.Insights[0].Title != valid.Title || len(published.Priorities) != 0 {
		t.Fatalf("published output retained rejected text: %#v", published)
	}
	if err := f.service.Publish(ctx, f.owner, "week", policy.Shared, input, Narrative{Lead: invalid}, "test-model"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("fully invalid output published: %v", err)
	}
}

func TestHistoryCapsAndKeepsPrivateSnapshotsScoped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	source := f.source(t, f.owner, policy.Personal, "private-history")
	if _, err := f.finance.Create(ctx, f.owner, finance.Draft{Kind: finance.Income, Visibility: policy.Personal, Label: "Income", Category: "Income", Date: "2026-07-20", AmountText: "10", Provenance: financeProvenance(source)}); err != nil {
		t.Fatal(err)
	}
	input, err := f.service.BuildContext(ctx, f.owner, policy.Personal)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 13; i++ {
		output := narrativeWithTestInsight(deterministic(input.Facts, time.Now()), input.Facts, input.Signals)
		if err := f.service.Publish(ctx, f.owner, "brief", policy.Personal, input, output, "test-model"); err != nil {
			t.Fatal(err)
		}
	}
	history, err := f.service.History(ctx, f.owner, "brief", policy.Personal)
	if err != nil || len(history) != 12 || history[0].Model != "test-model" {
		t.Fatalf("history=%#v err=%v", history, err)
	}
	partnerHistory, err := f.service.History(ctx, f.partner, "brief", policy.Personal)
	if err != nil || len(partnerHistory) != 0 {
		t.Fatalf("partner history=%#v err=%v", partnerHistory, err)
	}
	if err := f.sources.Delete(ctx, f.owner, source.ID); err != nil {
		t.Fatal(err)
	}
	history, err = f.service.History(ctx, f.owner, "brief", policy.Personal)
	if err != nil || len(history) != 0 {
		t.Fatalf("deleted-source history=%#v err=%v", history, err)
	}
}

func TestPublishRejectsEvidenceRemovedBeforeItsWriteTransaction(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	source := f.source(t, f.owner, policy.Shared, "shared-coaching-source")
	if _, err := f.finance.Create(ctx, f.owner, finance.Draft{Kind: finance.Spending, Visibility: policy.Shared, Label: "Food", Category: "Food", Date: "2026-07-20", AmountText: "10", Provenance: financeProvenance(source)}); err != nil {
		t.Fatal(err)
	}
	expected, err := f.service.BuildContext(ctx, f.owner, policy.Shared)
	if err != nil {
		t.Fatal(err)
	}
	output := deterministic(expected.Facts, time.Now(), expected.Signals...)
	if err := f.sources.Delete(ctx, f.owner, source.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Publish(ctx, f.owner, "brief", policy.Shared, expected, output, "test-model"); !errors.Is(err, ErrStale) {
		t.Fatalf("publish after source removal = %v, want ErrStale", err)
	}
	for _, table := range []string{"coaching_cache", "coaching_history"} {
		var count int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s retained %d rows after stale publish", table, count)
		}
	}
}

func TestVisibleUnitAndCalendarConflictsAreExplainedWithoutPrivateInfluence(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for index, unit := range []string{"mg/dL", "mmol/L"} {
		source := f.source(t, f.owner, policy.Shared, "glucose-"+unit)
		if _, err := f.health.CreateObservation(ctx, f.owner, health.ObservationDraft{Visibility: policy.Shared, Subject: "Owner", Analyte: "Glucose", ObservedOn: fmt.Sprintf("2026-07-%02d", 16+index), Value: "5", Unit: unit, Provenance: healthProvenance(source)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, title := range []string{"Appointment A", "Appointment B"} {
		source := f.source(t, f.owner, policy.Shared, title)
		if _, err := f.planning.CreateEvent(ctx, f.owner, planning.EventDraft{Visibility: policy.Shared, Title: title, AllDay: true, StartsOn: "2026-07-20", Status: "planned", OwnerIDs: []string{f.owner.ActorID}, Provenance: planningProvenance(source)}); err != nil {
			t.Fatal(err)
		}
	}
	context, err := f.service.BuildContext(ctx, f.owner, policy.Shared)
	if err != nil {
		t.Fatal(err)
	}
	unitIssues, calendarIssues := 0, 0
	for _, fact := range context.Facts {
		if strings.Contains(fact.Issue, "units differ") {
			unitIssues++
		}
		if strings.Contains(fact.Issue, "overlapping events") {
			calendarIssues++
		}
	}
	if unitIssues != 2 || calendarIssues != 2 {
		t.Fatalf("conflicts units=%d calendar=%d facts=%#v", unitIssues, calendarIssues, context.Facts)
	}
}

func TestCacheStalesOnSharedRevisionButIgnoresPartnerPrivateAndHardInvalidatesDeletedEvidence(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	source := f.source(t, f.owner, policy.Shared, "salary")
	if _, err := f.finance.Create(ctx, f.owner, finance.Draft{Kind: finance.Income, Visibility: policy.Shared, Label: "Salary", Category: "Income", Date: "2026-07-17", AmountText: "5000", Provenance: financeProvenance(source)}); err != nil {
		t.Fatal(err)
	}
	input, err := f.service.BuildContext(ctx, f.owner, policy.Shared)
	if err != nil {
		t.Fatal(err)
	}
	output := narrativeWithTestInsight(deterministic(input.Facts, time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)), input.Facts, input.Signals)
	if err := f.service.Publish(ctx, f.owner, "brief", policy.Shared, input, output, "test-model"); err != nil {
		t.Fatal(err)
	}
	overview, err := f.service.Overview(ctx, f.owner, time.Now())
	if err != nil || !overview.SharedCache.Found || overview.SharedCache.Stale {
		t.Fatalf("initial cache=%#v err=%v", overview.SharedCache, err)
	}
	private := f.source(t, f.partner, policy.Personal, "partner-private")
	if _, err := f.finance.Create(ctx, f.partner, finance.Draft{Kind: finance.Spending, Visibility: policy.Personal, Label: "Private", Category: "Other", Date: "2026-07-18", AmountText: "3", Provenance: financeProvenance(private)}); err != nil {
		t.Fatal(err)
	}
	overview, _ = f.service.Overview(ctx, f.owner, time.Now())
	if overview.SharedCache.Stale {
		t.Fatal("partner private record invalidated shared cache")
	}
	second := f.source(t, f.owner, policy.Shared, "shared-event")
	if _, err := f.planning.CreateEvent(ctx, f.owner, planning.EventDraft{Visibility: policy.Shared, Title: "Trip", AllDay: true, StartsOn: "2026-07-21", Status: "planned", Provenance: planningProvenance(second)}); err != nil {
		t.Fatal(err)
	}
	overview, _ = f.service.Overview(ctx, f.owner, time.Now())
	if !overview.SharedCache.Found || !overview.SharedCache.Stale {
		t.Fatalf("shared drift cache=%#v", overview.SharedCache)
	}
	foundLiveDate := false
	for _, item := range overview.Shared.Dates {
		if strings.Contains(item.Title, "Trip") {
			foundLiveDate = true
		}
	}
	if !foundLiveDate {
		t.Fatalf("stale cache hid live deterministic dates: %#v", overview.Shared.Dates)
	}
	if err := f.sources.Delete(ctx, f.owner, source.ID); err != nil {
		t.Fatal(err)
	}
	overview, _ = f.service.Overview(ctx, f.owner, time.Now())
	if overview.SharedCache.Found {
		t.Fatal("deleted evidence wording remained cached")
	}
}

func TestCacheRejectsOutdatedVersions(t *testing.T) {
	for _, test := range []struct {
		name   string
		column string
		value  string
	}{
		{name: "prompt", column: "prompt_version", value: "coaching-v1"},
		{name: "schema", column: "schema_version", value: "schema-v0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			source := f.source(t, f.owner, policy.Shared, "salary")
			if _, err := f.finance.Create(ctx, f.owner, finance.Draft{Kind: finance.Income, Visibility: policy.Shared, Label: "Salary", Category: "Income", Date: "2026-07-17", AmountText: "5000", Provenance: financeProvenance(source)}); err != nil {
				t.Fatal(err)
			}
			input, err := f.service.BuildContext(ctx, f.owner, policy.Shared)
			if err != nil {
				t.Fatal(err)
			}
			output := narrativeWithTestInsight(deterministic(input.Facts, time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)), input.Facts, input.Signals)
			if err := f.service.Publish(ctx, f.owner, "brief", policy.Shared, input, output, "test-model"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Exec(`UPDATE coaching_cache SET `+test.column+`=?`, test.value); err != nil {
				t.Fatal(err)
			}
			overview, err := f.service.Overview(ctx, f.owner, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if overview.SharedCache.Found {
				t.Fatalf("outdated cache remained visible: %#v", overview.SharedCache)
			}
			var remaining int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM coaching_cache`).Scan(&remaining); err != nil {
				t.Fatal(err)
			}
			if remaining != 0 {
				t.Fatalf("outdated cache rows=%d", remaining)
			}
		})
	}
}

func TestNudgeIsIdempotentAndFollowupIsExplicit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	source := f.source(t, f.owner, policy.Personal, "incomplete")
	record, err := f.finance.Create(ctx, f.owner, finance.Draft{Kind: finance.Spending, Visibility: policy.Personal, Label: "Groceries", Category: "Food", IncompleteNote: "amount needs correction", Provenance: financeProvenance(source)})
	if err != nil {
		t.Fatal(err)
	}
	first, err := f.service.EnsureNudge(ctx, f.owner, "finance", record.ID, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.service.EnsureNudge(ctx, f.owner, "finance", record.ID, source.ID)
	if err != nil || first.ID != second.ID || first.FollowUpEnabled {
		t.Fatalf("nudges %#v %#v err=%v", first, second, err)
	}
	if err := f.service.UpdateNudge(ctx, f.owner, first.ID, "follow-up-email-sent"); err != nil {
		t.Fatal(err)
	}
	current, err := f.service.Nudge(ctx, f.owner, "finance", record.ID)
	if err != nil || current.FollowUpEmailSent {
		t.Fatal("follow-up sent without opt-in")
	}
	if err := f.service.UpdateNudge(ctx, f.owner, first.ID, "enable-follow-up"); err != nil {
		t.Fatal(err)
	}
	if err := f.service.UpdateNudge(ctx, f.owner, first.ID, "follow-up-email-sent"); err != nil {
		t.Fatal(err)
	}
	current, _ = f.service.Nudge(ctx, f.owner, "finance", record.ID)
	if !current.FollowUpEnabled || !current.FollowUpEmailSent {
		t.Fatalf("explicit follow-up state=%#v", current)
	}
	if _, err := f.db.Exec(`UPDATE finance_spending SET active=0,version=version+1,updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), record.ID); err != nil {
		t.Fatal(err)
	}
	if nudges, err := f.service.ListNudges(ctx, f.owner); err != nil || len(nudges) != 0 {
		t.Fatalf("stale nudges=%#v err=%v", nudges, err)
	}
	var state string
	if err := f.db.QueryRow(`SELECT state FROM coaching_nudges WHERE id=?`, first.ID).Scan(&state); err != nil || state != "stale" {
		t.Fatalf("stale state=%q err=%v", state, err)
	}
}

func TestContextDropsPrivacyChangedSourcesAndInactiveRecords(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	source := f.source(t, f.owner, policy.Shared, "shared source")
	record, err := f.finance.Create(ctx, f.owner, finance.Draft{Kind: finance.Spending, Visibility: policy.Shared, Label: "Shared expense", Category: "Home", Date: "2026-07-18", AmountText: "100", Provenance: financeProvenance(source)})
	if err != nil {
		t.Fatal(err)
	}
	contextBefore, err := f.service.BuildContext(ctx, f.owner, policy.Shared)
	if err != nil || len(contextBefore.Facts) != 1 {
		t.Fatalf("initial context facts=%d err=%v", len(contextBefore.Facts), err)
	}
	if _, err := f.db.Exec(`UPDATE sources SET visibility='personal',updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), source.ID); err != nil {
		t.Fatal(err)
	}
	privateSource, err := f.service.BuildContext(ctx, f.owner, policy.Shared)
	if err != nil || len(privateSource.Facts) != 0 {
		t.Fatalf("privacy-changed source facts=%d err=%v", len(privateSource.Facts), err)
	}
	if _, err := f.db.Exec(`UPDATE sources SET visibility='shared',updated_at=? WHERE id=?; UPDATE finance_spending SET active=0,version=version+1,updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), source.ID, time.Now().UTC().Format(time.RFC3339Nano), record.ID); err != nil {
		t.Fatal(err)
	}
	inactive, err := f.service.BuildContext(ctx, f.owner, policy.Shared)
	if err != nil || len(inactive.Facts) != 0 {
		t.Fatalf("inactive record facts=%d err=%v", len(inactive.Facts), err)
	}
}
