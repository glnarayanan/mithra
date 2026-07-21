package coaching

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
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

func TestWeekClassifiesTypedRecordsOnceAndKeepsPrivateRecordsOut(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	asOf := time.Now().UTC()
	date := func(days int) string { return asOf.AddDate(0, 0, days).Format("2006-01-02") }

	oldSource := f.source(t, f.owner, policy.Shared, "old import")
	old, err := f.finance.Create(ctx, f.owner, finance.Draft{Kind: finance.Spending, Visibility: policy.Shared, Label: "April groceries", Category: "Food", Date: date(-90), AmountText: "1200", Provenance: financeProvenance(oldSource)})
	if err != nil {
		t.Fatal(err)
	}
	oldCancelledSource := f.source(t, f.owner, policy.Shared, "old cancelled import")
	oldCancelled, err := f.planning.CreateEvent(ctx, f.owner, planning.EventDraft{Visibility: policy.Shared, Title: "Old cancelled trip", AllDay: true, StartsOn: date(-90), Status: "cancelled", Provenance: planningProvenance(oldCancelledSource)})
	if err != nil {
		t.Fatal(err)
	}

	futureSource := f.source(t, f.owner, policy.Shared, "future event")
	future, err := f.planning.CreateEvent(ctx, f.owner, planning.EventDraft{Visibility: policy.Shared, Title: "School meeting", AllDay: true, StartsOn: date(4), Status: "planned", Provenance: planningProvenance(futureSource)})
	if err != nil {
		t.Fatal(err)
	}

	completedSource := f.source(t, f.owner, policy.Shared, "completion")
	completed, err := f.planning.CreateEvent(ctx, f.owner, planning.EventDraft{Visibility: policy.Shared, Title: "Renew insurance", AllDay: true, StartsOn: date(-10), Status: "planned", Provenance: planningProvenance(completedSource)})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.planning.CompleteEvent(ctx, f.owner, completed.ID, completed.Version); err != nil {
		t.Fatal(err)
	}

	cancelledSource := f.source(t, f.owner, policy.Shared, "cancellation")
	cancelled, err := f.planning.CreateEvent(ctx, f.owner, planning.EventDraft{Visibility: policy.Shared, Title: "Cancelled visit", AllDay: true, StartsOn: date(-2), Status: "cancelled", Provenance: planningProvenance(cancelledSource)})
	if err != nil {
		t.Fatal(err)
	}

	baseSource := f.source(t, f.owner, policy.Shared, "correction")
	base, err := f.finance.Create(ctx, f.owner, finance.Draft{Kind: finance.Spending, Visibility: policy.Shared, Label: "Corrected purchase", Category: "Home", Date: date(-14), AmountText: "20", Provenance: financeProvenance(baseSource)})
	if err != nil {
		t.Fatal(err)
	}
	corrected, err := f.finance.Correct(ctx, f.owner, finance.Spending, base.ID, base.Version, finance.Draft{Visibility: policy.Shared, Label: "Corrected purchase", Category: "Home", Date: date(-14), AmountText: "25", Provenance: financeProvenance(baseSource)})
	if err != nil {
		t.Fatal(err)
	}

	firstSource := f.source(t, f.owner, policy.Shared, "glucose one")
	secondSource := f.source(t, f.owner, policy.Shared, "glucose two")
	for _, row := range []struct {
		source     storage.Source
		date, unit string
	}{{firstSource, date(-2), "mg/dL"}, {secondSource, date(-1), "mmol/L"}} {
		if _, err := f.health.CreateObservation(ctx, f.owner, health.ObservationDraft{Visibility: policy.Shared, Subject: "Owner", Analyte: "Glucose", ObservedOn: row.date, Value: "5", Unit: row.unit, Provenance: healthProvenance(row.source)}); err != nil {
			t.Fatal(err)
		}
	}
	privateSource := f.source(t, f.partner, policy.Personal, "partner private")
	if _, err := f.finance.Create(ctx, f.partner, finance.Draft{Kind: finance.Spending, Visibility: policy.Personal, Label: "Partner private", Category: "Private", Date: "2026-07-18", AmountText: "7", Provenance: financeProvenance(privateSource)}); err != nil {
		t.Fatal(err)
	}
	labelSource := f.source(t, f.owner, policy.Shared, "label")
	if _, err := f.finance.Create(ctx, f.owner, finance.Draft{Kind: finance.Obligation, Visibility: policy.Shared, Label: "Insurance renewal", Category: "Insurance", Date: date(10), AmountText: "5000", Status: "pending", Provenance: financeProvenance(labelSource)}); err != nil {
		t.Fatal(err)
	}

	review, err := f.service.Week(ctx, f.owner, asOf)
	if err != nil {
		t.Fatal(err)
	}
	sections := map[string]string{}
	for _, section := range []struct {
		name   string
		events []ReviewEvent
	}{{"change", review.Shared.Changes}, {"upcoming", review.Shared.Upcoming}, {"issue", review.Shared.Issues}} {
		for _, event := range section.events {
			if previous, exists := sections[event.Fact.RecordID]; exists {
				t.Fatalf("record %s appears in %s and %s", event.Title, previous, section.name)
			}
			sections[event.Fact.RecordID] = section.name
		}
	}
	for _, insight := range review.Shared.Insights {
		for _, evidenceID := range insight.EvidenceIDs {
			for _, section := range []struct {
				name   string
				events []ReviewEvent
			}{{"change", review.Shared.Changes}, {"upcoming", review.Shared.Upcoming}, {"issue", review.Shared.Issues}} {
				for _, event := range section.events {
					if event.EvidenceID == evidenceID {
						t.Fatalf("evidence %s appears in insights and %s", evidenceID, section.name)
					}
				}
			}
		}
	}
	if _, exists := sections[old.ID]; exists {
		t.Fatalf("old import appeared in %q", sections[old.ID])
	}
	if _, exists := sections[oldCancelled.ID]; exists {
		t.Fatalf("old cancelled import appeared in %q", sections[oldCancelled.ID])
	}
	if sections[future.ID] != "upcoming" {
		t.Fatalf("future event section = %q", sections[future.ID])
	}
	if sections[completed.ID] != "change" || sections[cancelled.ID] != "change" || sections[corrected.ID] != "change" {
		t.Fatalf("completion/cancellation sections = %#v", sections)
	}
	if len(review.Shared.Issues) == 0 {
		t.Fatal("incompatible health units did not produce a deterministic issue")
	}
	for _, signal := range review.Shared.Context.Signals {
		if signal.Kind == "health_series" {
			t.Fatalf("incompatible health measurements reached a comparison: %#v", signal)
		}
	}
	foundHumanTitle := false
	for _, event := range review.Shared.Upcoming {
		if event.Title == "Insurance renewal" {
			foundHumanTitle = true
		}
		if strings.Contains(event.Title, "Insurance renewal Insurance") {
			t.Fatalf("malformed title = %q", event.Title)
		}
	}
	if !foundHumanTitle {
		t.Fatalf("typed finance title missing: %#v", review.Shared.Upcoming)
	}
	for _, fact := range review.Shared.Context.ReviewFacts {
		if strings.Contains(fact.Content, "Partner private") {
			t.Fatalf("shared review leaked private record: %#v", fact)
		}
	}
}

func TestWeekFormatsNumbersAndPercentagesWithoutCurrency(t *testing.T) {
	if got := formatScaledAmount(big.NewInt(1_234_500_000)); got != "1,234.5" {
		t.Fatalf("formatted number = %q", got)
	}
	if got := formatPercent(big.NewInt(2), big.NewInt(3)); got != "66.7%" {
		t.Fatalf("formatted percentage = %q", got)
	}
}

func TestWeekObservationUsesRecordedBudgetAndLowerOverallSpending(t *testing.T) {
	observation := reviewObservation([]Signal{
		{Kind: "finance_budget", Summary: "Spending recorded in Groceries through 21 Jul 2026 is 14,500 against the 18,000 budget recorded as July groceries budget, leaving 3,500. This is 80.6% of the recorded budget.", EvidenceIDs: []string{"budget"}},
		{Kind: "finance_month_to_date", Summary: "Spending recorded from 1 Jul 2026 through 21 Jul 2026 is 32,759, compared with 52,800 from 1 Jun 2026 through 21 Jun 2026.", EvidenceIDs: []string{"month"}},
	})
	if observation.Title != "Groceries need attention, not overall spending" || !strings.Contains(observation.Copy, "within 3,500") || !strings.Contains(observation.Copy, "adjust the recorded budget") {
		t.Fatalf("observation = %#v", observation)
	}
}

func TestWeekObservationDoesNotCallLowBudgetUseAWarning(t *testing.T) {
	observation := reviewObservation([]Signal{{Kind: "finance_budget", Summary: "Spending recorded in Groceries through 21 Jul 2026 is 4,000 against the 18,000 budget recorded as July groceries budget, leaving 14,000. This is 22.2% of the recorded budget."}})
	if observation.Title != "" || observation.Copy != "" {
		t.Fatalf("low budget use became a warning: %#v", observation)
	}
}

func TestWeekPrioritiesExcludeClosedChanges(t *testing.T) {
	completed := ReviewEvent{Fact: Fact{Family: "planning", Status: "completed"}, Title: "Completed plan", Copy: "Marked completed this week.", When: "2026-07-20"}
	upcoming := ReviewEvent{Fact: Fact{Family: "planning", Status: "planned"}, Title: "Review travel documents", Copy: "Preparation is due soon.", When: "2026-07-25"}
	priorities := reviewPriorities(ReviewScope{Changes: []ReviewEvent{completed}, Upcoming: []ReviewEvent{upcoming}})
	if len(priorities) != 1 || priorities[0].Title != upcoming.Title {
		t.Fatalf("closed work displaced an upcoming priority: %#v", priorities)
	}
}

func TestWeekPrioritiesKeepUpdatedOverdueWork(t *testing.T) {
	overdue := ReviewEvent{Fact: Fact{Family: "planning", Status: "planned"}, Title: "Renew a document", Copy: "Updated this week.", When: "2026-07-20", Overdue: true}
	priorities := reviewPriorities(ReviewScope{Changes: []ReviewEvent{overdue}})
	if len(priorities) != 1 || priorities[0].Title != overdue.Title {
		t.Fatalf("updated overdue work vanished: %#v", priorities)
	}
	status := reviewStatus(ReviewScope{Context: Context{Scope: string(policy.Shared), ReviewFacts: []Fact{{Family: "planning"}}}, Changes: []ReviewEvent{overdue}})
	if status.Label != "Needs attention" {
		t.Fatalf("updated overdue status = %#v", status)
	}
}

func TestWeekStatusDoesNotClaimEmptyHouseholdIsOnTrack(t *testing.T) {
	status := reviewStatus(ReviewScope{Context: Context{Scope: string(policy.Shared)}})
	if status.Label != "No shared records yet" {
		t.Fatalf("empty status = %#v", status)
	}
}

func TestWeekDoesNotGroupAmbiguousRenewals(t *testing.T) {
	planning := ReviewEvent{Fact: Fact{Family: "planning", Visibility: policy.Shared}, Title: "Insurance renewal review", When: "2026-07-27"}
	first := ReviewEvent{Fact: Fact{Family: "finance", Kind: "obligation", Visibility: policy.Shared}, Title: "Insurance renewal", When: "2026-07-26"}
	second := ReviewEvent{Fact: Fact{Family: "finance", Kind: "obligation", Visibility: policy.Shared}, Title: "Insurance renewal", When: "2026-07-28"}
	grouped := groupReviewUpcoming([]ReviewEvent{planning, first, second})
	if len(grouped) != 3 {
		t.Fatalf("ambiguous renewals were grouped: %#v", grouped)
	}
}

func TestWeekGroupingIsIndependentOfPlanningOrder(t *testing.T) {
	first := ReviewEvent{Fact: Fact{Family: "planning", Visibility: policy.Shared}, Title: "Insurance renewal review", When: "2026-07-25"}
	closest := ReviewEvent{Fact: Fact{Family: "planning", Visibility: policy.Shared}, Title: "Insurance renewal review", When: "2026-07-27"}
	obligation := ReviewEvent{Fact: Fact{Family: "finance", Kind: "obligation", Visibility: policy.Shared}, Title: "Insurance renewal", When: "2026-07-28"}
	for _, events := range [][]ReviewEvent{{first, closest, obligation}, {closest, first, obligation}} {
		grouped := groupReviewUpcoming(events)
		found := false
		for _, event := range grouped {
			if event.Domain == "Planning + finance" {
				found = event.When == closest.When
			}
		}
		if !found {
			t.Fatalf("closest planning record was not grouped: %#v", grouped)
		}
	}
}

func TestWeekHealthComparisonNamesTheMeasurement(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	asOf := time.Now().UTC()
	for index, value := range []string{"5", "6"} {
		source := f.source(t, f.owner, policy.Shared, "reading")
		if _, err := f.health.CreateObservation(ctx, f.owner, health.ObservationDraft{Visibility: policy.Shared, Subject: "Owner", Analyte: "Glucose", ObservedOn: asOf.AddDate(0, 0, -2+index).Format("2006-01-02"), Value: value, Unit: "mg/dL", Provenance: healthProvenance(source)}); err != nil {
			t.Fatal(err)
		}
	}
	review, err := f.service.Week(ctx, f.owner, asOf)
	if err != nil {
		t.Fatal(err)
	}
	foundSignal := false
	for _, signal := range review.Shared.Context.Signals {
		if signal.Kind == "health_series" {
			if !strings.Contains(signal.Summary, "Glucose for Owner") || !strings.Contains(signal.Summary, "mg/dL") {
				t.Fatalf("health signal = %#v", signal)
			}
			foundSignal = true
		}
	}
	if !foundSignal {
		t.Fatal("missing named health comparison")
	}
	for _, insight := range review.Shared.Insights {
		if insight.Title == "Glucose" {
			return
		}
	}
	t.Fatalf("review insight did not name the measurement: %#v", review.Shared.Insights)
}

func TestWeekDoesNotTreatSpendingOrHealthObservationsAsOverdue(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	asOf := time.Now().UTC()
	date := asOf.AddDate(0, 0, -1).Format("2006-01-02")
	spendingSource := f.source(t, f.owner, policy.Shared, "recent spending")
	if _, err := f.finance.Create(ctx, f.owner, finance.Draft{Kind: finance.Spending, Visibility: policy.Shared, Label: "Groceries", Category: "Food", Date: date, AmountText: "25", Provenance: financeProvenance(spendingSource)}); err != nil {
		t.Fatal(err)
	}
	healthSource := f.source(t, f.owner, policy.Shared, "recent glucose")
	if _, err := f.health.CreateObservation(ctx, f.owner, health.ObservationDraft{Visibility: policy.Shared, Subject: "Owner", Analyte: "Glucose", ObservedOn: date, Value: "5", Unit: "mg/dL", Provenance: healthProvenance(healthSource)}); err != nil {
		t.Fatal(err)
	}
	review, err := f.service.Week(ctx, f.owner, asOf)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range append(append([]ReviewEvent{}, review.Shared.Changes...), review.Shared.Upcoming...) {
		if event.Title == "Groceries" || event.Title == "Glucose" || strings.Contains(event.Copy, "Past its recorded date") {
			t.Fatalf("ordinary record appeared as overdue work: %#v", event)
		}
	}
}

func TestWeekGroupsCompatiblePlanningAndFinanceRenewal(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	asOf := time.Now().UTC()
	date := func(days int) string { return asOf.AddDate(0, 0, days).Format("2006-01-02") }
	planningSource := f.source(t, f.owner, policy.Shared, "insurance review")
	if _, err := f.planning.CreateEvent(ctx, f.owner, planning.EventDraft{Visibility: policy.Shared, Title: "Insurance renewal review", AllDay: true, StartsOn: date(6), Status: "planned", Provenance: planningProvenance(planningSource)}); err != nil {
		t.Fatal(err)
	}
	financeSource := f.source(t, f.owner, policy.Shared, "insurance payment")
	if _, err := f.finance.Create(ctx, f.owner, finance.Draft{Kind: finance.Obligation, Visibility: policy.Shared, Label: "Insurance renewal", Category: "Insurance", Date: date(7), AmountText: "5000", Status: "pending", Provenance: financeProvenance(financeSource)}); err != nil {
		t.Fatal(err)
	}
	review, err := f.service.Week(ctx, f.owner, asOf)
	if err != nil {
		t.Fatal(err)
	}
	var grouped []ReviewEvent
	for _, event := range review.Shared.Upcoming {
		if event.Title == "Insurance renewal" {
			grouped = append(grouped, event)
		}
	}
	if len(grouped) != 1 || len(grouped[0].EvidenceIDs) != 2 || !strings.Contains(grouped[0].Copy, "Payment due") {
		t.Fatalf("renewal was not grouped conservatively: %#v", review.Shared.Upcoming)
	}
}

func TestSignalsUseVisibleTypedRecordsAndTrustOnlyFullyCitedNumbers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.service.now = func() time.Time { return time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC) }
	for _, row := range []struct{ date, amount, category string }{{"2026-06-20", "10", "Food"}, {"2026-06-24", "100", "Food"}, {"2026-07-20", "20", "Food"}, {"2026-07-19", "70", "Travel"}} {
		source := f.source(t, f.owner, policy.Shared, row.date)
		if _, err := f.finance.Create(ctx, f.owner, finance.Draft{Kind: finance.Spending, Visibility: policy.Shared, Label: row.category, Category: row.category, Date: row.date, AmountText: row.amount, Provenance: financeProvenance(source)}); err != nil {
			t.Fatal(err)
		}
	}
	budgetSource := f.source(t, f.owner, policy.Shared, "budget")
	if _, err := f.finance.Create(ctx, f.owner, finance.Draft{Kind: finance.Budget, Visibility: policy.Shared, Label: "July food budget", Category: "Food", Date: "2026-07-01", EndDate: "2026-07-31", AmountText: "100", Provenance: financeProvenance(budgetSource)}); err != nil {
		t.Fatal(err)
	}
	obligationSource := f.source(t, f.owner, policy.Shared, "obligation")
	if _, err := f.finance.Create(ctx, f.owner, finance.Draft{Kind: finance.Obligation, Visibility: policy.Shared, Label: "Insurance renewal", Category: "Insurance", Date: "2026-07-28", Status: "pending", AmountText: "30", Provenance: financeProvenance(obligationSource)}); err != nil {
		t.Fatal(err)
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
	for _, kind := range []string{"finance_month_to_date", "finance_budget", "finance_obligations", "health_series", "planning_upcoming"} {
		if len(kinds[kind].EvidenceIDs) == 0 {
			t.Fatalf("missing %s signal: %#v", kind, input.Signals)
		}
	}
	if _, exists := kinds["weekly_activity"]; exists {
		t.Fatalf("record-ingestion activity should not be a coaching signal: %#v", input.Signals)
	}
	financeSignal := kinds["finance_month_to_date"]
	if !strings.Contains(financeSignal.Summary, "90") || !strings.Contains(financeSignal.Summary, "10") || strings.Contains(financeSignal.Summary, "110") {
		t.Fatalf("finance signal = %#v", financeSignal)
	}
	if !strings.Contains(kinds["finance_budget"].Summary, "80") || !strings.Contains(kinds["finance_budget"].Summary, "20%") {
		t.Fatalf("budget signal = %#v", kinds["finance_budget"])
	}
	if !strings.Contains(kinds["finance_obligations"].Summary, "30") || !strings.Contains(kinds["finance_obligations"].Summary, "28 Jul 2026") {
		t.Fatalf("obligation signal = %#v", kinds["finance_obligations"])
	}
	fact := Fact{EvidenceID: "a", Content: "Rent", Date: "2026-07-20"}
	item := Item{Title: "Rent", Copy: "Recorded 999 this month.", EvidenceIDs: []string{"a"}}
	if err := validateNarrative(Narrative{Lead: item}, map[string]Fact{"a": fact}, Signal{Summary: "Recorded 999 this month.", EvidenceIDs: []string{"a"}}); err != nil {
		t.Fatalf("trusted signal number rejected: %v", err)
	}
	if !errors.Is(validateNarrative(Narrative{Lead: item}, map[string]Fact{"a": fact}, Signal{Summary: "Recorded 999 this month.", EvidenceIDs: []string{"a", "b"}}), ErrUnsupported) {
		t.Fatal("partially cited signal number accepted")
	}
	comparison := Item{Title: "Spending increased", Copy: "Recorded spending increased from 10 to 90.", EvidenceIDs: financeSignal.EvidenceIDs}
	allowed := make(map[string]Fact, len(input.Facts))
	for _, fact := range input.Facts {
		allowed[fact.EvidenceID] = fact
	}
	if err := validateNarrative(Narrative{Lead: comparison}, allowed, financeSignal); err != nil {
		t.Fatalf("factual signal comparison rejected: %v", err)
	}
	insight := Item{Title: "Spending comparison", Copy: financeSignal.Summary, When: financeSignal.Period, EvidenceIDs: financeSignal.EvidenceIDs}
	natural := Narrative{Lead: Item{Title: "July spending rose", Copy: "Recorded spending rose from 10 to 90.0 between June and July.", EvidenceIDs: financeSignal.EvidenceIDs}, Insights: []Item{insight}}
	if err := f.service.Publish(ctx, f.owner, "brief", policy.Shared, input, natural, "test-model"); err != nil {
		t.Fatalf("natural, cited signal wording did not publish: %v", err)
	}
	if err := f.service.Publish(ctx, f.owner, "week", policy.Shared, input, Narrative{Lead: natural.Lead}, "test-model"); err != nil {
		t.Fatalf("grounded lead did not receive a deterministic signal insight: %v", err)
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
	if err := f.service.Publish(ctx, f.owner, "week", policy.Shared, input, unknown, "test-model"); err != nil {
		t.Fatalf("grounded lead with unknown signal evidence did not receive deterministic signal insight: %v", err)
	}
	differentCopy := normalized
	differentCopy.Insights[0].Copy += " extra"
	if err := f.service.Publish(ctx, f.owner, "week", policy.Shared, input, differentCopy, "test-model"); err != nil {
		t.Fatalf("grounded lead with changed signal summary did not receive deterministic signal insight: %v", err)
	}
	sharedView := Narrative{Lead: Item{Title: "Monthly spending pattern", Copy: "Recent activity listed spending from 10 to 90 against prior records.", EvidenceIDs: financeSignal.EvidenceIDs}}
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

func TestSignalsKeepThreeComparableHealthSeries(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.service.now = func() time.Time { return time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC) }
	for index, analyte := range []string{"Glucose", "Weight", "Resting heart rate"} {
		for point, row := range []struct{ date, value string }{{"2026-06-01", "10"}, {"2026-07-01", "9"}} {
			source := f.source(t, f.owner, policy.Personal, fmt.Sprintf("health-%d-%d", index, point))
			if _, err := f.health.CreateObservation(ctx, f.owner, health.ObservationDraft{Visibility: policy.Personal, Subject: "Owner", Analyte: analyte, ObservedOn: row.date, Value: row.value, Unit: "u", Provenance: healthProvenance(source)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	input, err := f.service.BuildContext(ctx, f.owner, policy.Personal)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, signal := range input.Signals {
		if signal.Kind == "health_series" {
			count++
		}
	}
	if count != 3 {
		t.Fatalf("health signals=%d, want 3: %#v", count, input.Signals)
	}
}

func TestMonthToDateUsesAValidSharedCutoffAndSkipsInvalidDates(t *testing.T) {
	for _, test := range []struct {
		name, currentDate, priorDate, excludedDate, currentCutoff, priorCutoff string
		asOf                                                                   time.Time
	}{
		{"march versus february", "2026-03-28", "2026-02-28", "2026-03-30", "2026-03-28", "2026-02-28", time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)},
		{"april versus march", "2026-04-30", "2026-03-30", "2026-03-31", "2026-04-30", "2026-03-30", time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			f.service.now = func() time.Time { return test.asOf }
			for _, row := range []struct{ date, amount string }{{test.currentDate, "20"}, {test.priorDate, "10"}, {test.excludedDate, "100"}, {"", "999"}} {
				source := f.source(t, f.owner, policy.Shared, "spending-"+row.date+row.amount)
				if _, err := f.finance.Create(ctx, f.owner, finance.Draft{Kind: finance.Spending, Visibility: policy.Shared, Label: "Food", Category: "Food", Date: row.date, AmountText: row.amount, Provenance: financeProvenance(source)}); err != nil {
					t.Fatal(err)
				}
			}
			input, err := f.service.BuildContext(ctx, f.owner, policy.Shared)
			if err != nil {
				t.Fatal(err)
			}
			var summary string
			for _, signal := range input.Signals {
				if signal.Kind == "finance_month_to_date" {
					summary = signal.Summary
				}
			}
			if !strings.Contains(summary, displayDate(test.currentCutoff)) || !strings.Contains(summary, displayDate(test.priorCutoff)) || !strings.Contains(summary, "is 20, compared with 10") || strings.Contains(summary, "120") || strings.Contains(summary, "999") {
				t.Fatalf("month-to-date summary=%q", summary)
			}
		})
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
		if candidate.Kind == "finance_month_to_date" {
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
		if signal.Kind == "finance_month_to_date" {
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
		if signal.Kind == "finance_month_to_date" {
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
		if candidate.Kind == "finance_month_to_date" {
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
	if published.Lead.Title != expectedLead.Title || published.Lead.Copy != expectedLead.Copy || strings.Join(published.Lead.EvidenceIDs, ",") != strings.Join(expectedLead.EvidenceIDs, ",") || len(published.Insights) != 1 || published.Insights[0].Title != signalTitle(signal.Kind) || len(published.Priorities) != 0 {
		t.Fatalf("published output retained rejected text: %#v", published)
	}
	if err := f.service.Publish(ctx, f.owner, "week", policy.Shared, input, Narrative{Lead: invalid}, "test-model"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("fully invalid output published: %v", err)
	}
}

func TestPublishAddsExactSignalWhenModelParaphrasesIt(t *testing.T) {
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
		if candidate.Kind == "finance_month_to_date" {
			signal = candidate
		}
	}
	if len(signal.EvidenceIDs) == 0 {
		t.Fatalf("missing finance signal: %#v", input.Signals)
	}
	lead := Item{Title: "Food", Copy: "Food is recorded.", EvidenceIDs: []string{input.Facts[0].EvidenceID}}
	paraphrase := Item{Title: "Monthly pattern", Copy: "Spending is clearly better this month.", EvidenceIDs: signal.EvidenceIDs}
	if err := f.service.Publish(ctx, f.owner, "brief", policy.Shared, input, Narrative{Lead: lead, Insights: []Item{paraphrase}}, "test-model"); err != nil {
		t.Fatalf("grounded lead with paraphrased signal did not publish: %v", err)
	}
	var encoded string
	if err := f.db.QueryRow(`SELECT content_json FROM coaching_cache WHERE household_id='home' AND mode='brief' AND visibility='shared'`).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var published Narrative
	if err := json.Unmarshal([]byte(encoded), &published); err != nil {
		t.Fatal(err)
	}
	if published.Lead.Title != lead.Title || published.Lead.Copy != lead.Copy || strings.Join(published.Lead.EvidenceIDs, ",") != strings.Join(lead.EvidenceIDs, ",") || len(published.Insights) != 1 || published.Insights[0].Copy != signal.Summary || strings.Join(published.Insights[0].EvidenceIDs, ",") != strings.Join(signal.EvidenceIDs, ",") {
		t.Fatalf("published narrative=%#v", published)
	}
	fiveGrounded := []Item{lead, lead, lead, lead, lead}
	if err := f.service.Publish(ctx, f.owner, "week", policy.Shared, input, Narrative{Lead: lead, Insights: fiveGrounded}, "test-model"); err != nil {
		t.Fatalf("five grounded insights did not leave room for the exact signal: %v", err)
	}
	if err := f.db.QueryRow(`SELECT content_json FROM coaching_cache WHERE household_id='home' AND mode='week' AND visibility='shared'`).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(encoded), &published); err != nil {
		t.Fatal(err)
	}
	if len(published.Insights) != 5 || published.Insights[3].Copy != lead.Copy || published.Insights[4].Copy != signal.Summary || strings.Join(published.Insights[4].EvidenceIDs, ",") != strings.Join(signal.EvidenceIDs, ",") {
		t.Fatalf("five-insight fallback=%#v", published.Insights)
	}
}

func TestPublishAcceptsCanonicalHealthSignalSafetyBoundary(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.service.now = func() time.Time { return time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC) }
	for index, row := range []struct{ date, value string }{{"2026-06-01", "101"}, {"2026-07-01", "91"}} {
		source := f.source(t, f.owner, policy.Personal, fmt.Sprintf("health-safety-%d", index))
		if _, err := f.health.CreateObservation(ctx, f.owner, health.ObservationDraft{Visibility: policy.Personal, Subject: "Owner", Analyte: "Glucose", ObservedOn: row.date, Value: row.value, Unit: "mg/dL", Provenance: healthProvenance(source)}); err != nil {
			t.Fatal(err)
		}
	}
	input, err := f.service.BuildContext(ctx, f.owner, policy.Personal)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Signals) != 1 || input.Signals[0].Kind != "health_series" || !strings.Contains(input.Signals[0].Summary, "not health advice") {
		t.Fatalf("health signal=%#v", input.Signals)
	}
	signal := input.Signals[0]
	lead := factItem(input.Facts[0])
	insight := Item{Title: "Health pattern", Copy: signal.Summary, When: signal.Period, EvidenceIDs: signal.EvidenceIDs[:1]}
	if err := f.service.Publish(ctx, f.owner, "brief", policy.Personal, input, Narrative{Lead: lead, Insights: []Item{insight}}, "test-model"); err != nil {
		t.Fatalf("canonical health signal did not publish: %v", err)
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
	for _, fact := range context.ReviewFacts {
		if strings.Contains(fact.Issue, "units differ") {
			unitIssues++
		}
		if strings.Contains(fact.Issue, "overlapping events") {
			calendarIssues++
		}
	}
	if unitIssues != 2 || calendarIssues != 2 {
		t.Fatalf("conflicts units=%d calendar=%d facts=%#v", unitIssues, calendarIssues, context.ReviewFacts)
	}
	for _, fact := range context.Facts {
		if fact.Family == "health" {
			t.Fatalf("invalid health record reached coaching input: %#v", fact)
		}
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
