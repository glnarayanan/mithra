package coaching

import (
	"context"
	"database/sql"
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
	output := deterministic(input.Facts, time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC))
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
