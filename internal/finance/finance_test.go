package finance

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/storage"
)

type financeFixture struct {
	db      *sql.DB
	service *Service
	sources *storage.Service
	owner   policy.ActorScope
	partner policy.ActorScope
}

func newFinanceFixture(t *testing.T) financeFixture {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "mithra.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	stamp := "2026-07-01T00:00:00Z"
	for _, values := range [][]any{{"owner", "owner@example.com"}, {"partner", "partner@example.com"}, {"outsider", "outside@example.com"}} {
		if _, err := db.Exec(`INSERT INTO users(id,email,status,password_hash,created_at,updated_at) VALUES(?,?,'active','hash',?,?)`, values[0], values[1], stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	for _, values := range [][]any{{"home", "owner"}, {"other-home", "outsider"}} {
		if _, err := db.Exec(`INSERT INTO households(id,status,owner_user_id,created_at,updated_at) VALUES(?,'active',?,?,?)`, values[0], values[1], stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	for _, values := range [][]any{{"home", "owner", "owner"}, {"home", "partner", "adult"}, {"other-home", "outsider", "owner"}} {
		if _, err := db.Exec(`INSERT INTO household_members(household_id,user_id,role,created_at) VALUES(?,?,?,?)`, values[0], values[1], values[2], stamp); err != nil {
			t.Fatal(err)
		}
	}
	master := make([]byte, 32)
	for index := range master {
		master[index] = byte(index + 1)
	}
	sources, err := storage.New(db, filepath.Join(t.TempDir(), "sources"), master)
	if err != nil {
		t.Fatal(err)
	}
	return financeFixture{db: db, service: New(db), sources: sources, owner: policy.ActorScope{ActorID: "owner", HouseholdID: "home", Role: "owner"}, partner: policy.ActorScope{ActorID: "partner", HouseholdID: "home", Role: "adult"}}
}

func (fixture financeFixture) source(t *testing.T, actor policy.ActorScope, visibility policy.Visibility, content []byte) storage.Source {
	t.Helper()
	source, err := fixture.sources.Store(context.Background(), actor, content, storage.Metadata{Family: "csv", Version: 1, Visibility: visibility, LocatorKind: "source", LocatorValue: "household.csv"})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestGoldenFinanceFixtureProducesExactOfflineSummary(t *testing.T) {
	fixture := newFinanceFixture(t)
	content, err := os.ReadFile(filepath.Join("..", "..", "testdata", "imports", "finance", "household.csv"))
	if err != nil {
		t.Fatal(err)
	}
	source := fixture.source(t, fixture.owner, policy.Shared, content)
	rows, err := csv.NewReader(bytes.NewReader(content)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for index, row := range rows[1:] {
		kind := map[string]Kind{"income": Income, "spending": Spending, "asset": Asset, "liability": Liability, "budget": Budget, "obligation": Obligation}[row[0]]
		_, err := fixture.service.Create(context.Background(), fixture.owner, Draft{
			Kind: kind, Visibility: policy.Visibility(row[7]), Label: row[1], Category: row[2], Date: row[3], EndDate: row[4], Status: row[5], AmountText: row[6], CurrencyContext: row[8],
			Provenance: Provenance{SourceID: source.ID, SourceFamily: source.Family, SourceVersion: source.Version, LocatorKind: "row", LocatorValue: string(rune('2' + index)), GeneratedBy: "application", SchemaVersion: "finance-v1"},
		})
		if err != nil {
			t.Fatalf("create fixture row %d: %v", index+2, err)
		}
	}
	summary, err := fixture.service.Summarize(context.Background(), fixture.owner, AllRecords, time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	got := struct {
		Complete          int               `json:"complete"`
		Incomplete        int               `json:"incomplete"`
		Totals            map[string]string `json:"totals"`
		GroceriesPrevious string            `json:"groceries_previous"`
		GroceriesCurrent  string            `json:"groceries_current"`
		GroceriesChange   string            `json:"groceries_change"`
		Upcoming          []string          `json:"upcoming"`
	}{Complete: summary.Complete, Incomplete: summary.Incomplete, Totals: make(map[string]string)}
	for kind, total := range summary.Totals {
		got.Totals[string(kind)] = total.PlainString()
	}
	for _, trend := range summary.Trends {
		if trend.Category == "Groceries" {
			got.GroceriesPrevious, got.GroceriesCurrent, got.GroceriesChange = trend.Previous.PlainString(), trend.Current.PlainString(), trend.Change.PlainString()
		}
	}
	for _, record := range summary.Upcoming {
		got.Upcoming = append(got.Upcoming, record.Label)
	}
	gotJSON, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := os.ReadFile(filepath.Join("..", "..", "testdata", "imports", "finance", "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	var gotValue, wantValue any
	if json.Unmarshal(gotJSON, &gotValue) != nil || json.Unmarshal(wantJSON, &wantValue) != nil || !deepEqualJSON(gotValue, wantValue) {
		t.Fatalf("summary = %s\nwant %s", gotJSON, wantJSON)
	}
	if len(summary.Issues) != 1 || summary.Issues[0].Reason != "amount needs correction" || summary.Issues[0].SourceID != source.ID {
		t.Fatalf("issues = %#v", summary.Issues)
	}
}

func TestPersonalFinanceIsAbsentFromPartnerAndSharedReads(t *testing.T) {
	fixture := newFinanceFixture(t)
	personal := fixture.source(t, fixture.owner, policy.Personal, []byte("private balance"))
	shared := fixture.source(t, fixture.owner, policy.Shared, []byte("shared balance"))
	privateRecord, err := fixture.service.Create(context.Background(), fixture.owner, Draft{Kind: Asset, Visibility: policy.Personal, Label: "Private reserve", Date: "2026-07-01", AmountText: "900", Provenance: Provenance{SourceID: personal.ID, SourceFamily: "csv", SourceVersion: 1, LocatorKind: "row", LocatorValue: "2"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Create(context.Background(), fixture.owner, Draft{Kind: Asset, Visibility: policy.Shared, Label: "Shared reserve", Date: "2026-07-01", AmountText: "100", Provenance: Provenance{SourceID: shared.ID, SourceFamily: "csv", SourceVersion: 1, LocatorKind: "row", LocatorValue: "2"}}); err != nil {
		t.Fatal(err)
	}
	partnerRecords, err := fixture.service.List(context.Background(), fixture.partner, AllRecords)
	if err != nil || len(partnerRecords) != 1 || partnerRecords[0].Label != "Shared reserve" {
		t.Fatalf("partner records = %#v, %v", partnerRecords, err)
	}
	ownerShared, err := fixture.service.List(context.Background(), fixture.owner, SharedRecords)
	if err != nil || len(ownerShared) != 1 || ownerShared[0].Visibility != policy.Shared {
		t.Fatalf("shared records = %#v, %v", ownerShared, err)
	}
	var indexedVisibility string
	db := fixture.db
	if err := db.QueryRow(`SELECT visibility FROM search_entries WHERE record_id=?`, privateRecord.ID).Scan(&indexedVisibility); err != nil || indexedVisibility != "personal" {
		t.Fatalf("private search scope = %q, %v", indexedVisibility, err)
	}
	outsider := policy.ActorScope{ActorID: "outsider", HouseholdID: "other-home", Role: "owner"}
	if records, err := fixture.service.List(context.Background(), outsider, AllRecords); err != nil || len(records) != 0 {
		t.Fatalf("other household records = %#v, %v", records, err)
	}
	if _, err := db.Exec(`UPDATE users SET status='disabled',disabled_at=updated_at WHERE id='owner'`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.List(context.Background(), fixture.owner, AllRecords); !errors.Is(err, policy.ErrUnauthorized) {
		t.Fatalf("disabled actor list error = %v", err)
	}
}

func TestCorrectionUsesOptimisticVersionAndInvalidatesOnlyItsScope(t *testing.T) {
	fixture := newFinanceFixture(t)
	source := fixture.source(t, fixture.owner, policy.Shared, []byte("shared spending"))
	record, err := fixture.service.Create(context.Background(), fixture.owner, Draft{Kind: Spending, Visibility: policy.Shared, Label: "Groceries", Category: "Food", Date: "2026-07-01", AmountText: "10", Provenance: Provenance{SourceID: source.ID, SourceFamily: "csv", SourceVersion: 1, LocatorKind: "row", LocatorValue: "2"}})
	if err != nil {
		t.Fatal(err)
	}
	db := fixture.db
	var sharedBefore, personalBefore int64
	if err := db.QueryRow(`SELECT shared_revision FROM household_revisions WHERE household_id='home'`).Scan(&sharedBefore); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT personal_revision FROM user_revisions WHERE user_id='owner'`).Scan(&personalBefore); err != nil {
		t.Fatal(err)
	}
	corrected, err := fixture.service.Correct(context.Background(), fixture.partner, Spending, record.ID, record.Version, Draft{Visibility: policy.Shared, Label: "Groceries corrected", Category: "Food", Date: "2026-07-01", AmountText: "12.50", Provenance: Provenance{SourceID: source.ID, SourceFamily: "csv", SourceVersion: 1, LocatorKind: "row", LocatorValue: "2", GeneratedBy: "user"}})
	if err != nil || corrected.SupersedesID != record.ID || corrected.Amount.PlainString() != "12.50" {
		t.Fatalf("correction = %#v, %v", corrected, err)
	}
	if _, err := fixture.service.Correct(context.Background(), fixture.owner, Spending, record.ID, record.Version, Draft{Visibility: policy.Shared, Label: "stale", Date: "2026-07-01", AmountText: "20", Provenance: Provenance{SourceID: source.ID, SourceFamily: "csv", SourceVersion: 1, LocatorKind: "row", LocatorValue: "2"}}); !errors.Is(err, policy.ErrUnauthorized) && !errors.Is(err, policy.ErrConflict) {
		t.Fatalf("stale correction error = %v", err)
	}
	var sharedAfter, personalAfter, oldSearch int64
	if err := db.QueryRow(`SELECT shared_revision FROM household_revisions WHERE household_id='home'`).Scan(&sharedAfter); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT personal_revision FROM user_revisions WHERE user_id='owner'`).Scan(&personalAfter); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM search_entries WHERE record_id=?`, record.ID).Scan(&oldSearch); err != nil {
		t.Fatal(err)
	}
	if sharedAfter <= sharedBefore || personalAfter != personalBefore || oldSearch != 0 {
		t.Fatalf("revisions/search after correction = shared %d>%d personal %d=%d old_search %d", sharedAfter, sharedBefore, personalAfter, personalBefore, oldSearch)
	}
}

func TestCurrencyContextAndDatabaseScopeBoundaries(t *testing.T) {
	if err := ValidateCurrencyContexts([]string{"USD", " usd ", ""}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCurrencyContexts([]string{"USD", "INR"}); !errors.Is(err, ErrCurrencyContexts) {
		t.Fatalf("mixed context error = %v", err)
	}
	fixture := newFinanceFixture(t)
	personal := fixture.source(t, fixture.owner, policy.Personal, []byte("private"))
	if _, err := fixture.service.Create(context.Background(), fixture.owner, Draft{Kind: Income, Visibility: policy.Shared, Label: "Too broad", Date: "2026-07-01", AmountText: "1", Provenance: Provenance{SourceID: personal.ID, SourceFamily: "csv", SourceVersion: 1, LocatorKind: "row", LocatorValue: "2"}}); err == nil {
		t.Fatal("shared record cited a personal source")
	}
}

func TestMissingDateExcludesRecordFromTotalsAndTrends(t *testing.T) {
	fixture := newFinanceFixture(t)
	source := fixture.source(t, fixture.owner, policy.Shared, []byte("undated spending"))
	if _, err := fixture.service.Create(context.Background(), fixture.owner, Draft{Kind: Spending, Visibility: policy.Shared, Label: "Undated repair", Category: "Home", AmountText: "42.25", Provenance: Provenance{SourceID: source.ID, SourceFamily: "csv", SourceVersion: 1, LocatorKind: "row", LocatorValue: "2"}}); err != nil {
		t.Fatal(err)
	}
	summary, err := fixture.service.Summarize(context.Background(), fixture.owner, AllRecords, time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got := summary.Totals[Spending].PlainString(); got != "0" {
		t.Fatalf("undated amount total = %s, want 0", got)
	}
	if summary.Incomplete != 1 || len(summary.Issues) != 1 || summary.Issues[0].Reason != "date is missing" || len(summary.Trends) != 0 {
		t.Fatalf("undated summary = %#v", summary)
	}
}

func TestSummarizeRangeBuildsZeroFilledMonthlyTrendline(t *testing.T) {
	fixture := newFinanceFixture(t)
	source := fixture.source(t, fixture.owner, policy.Shared, []byte("monthly groceries"))
	for index, entry := range []struct{ date, amount string }{{"2026-04-10", "100"}, {"2026-06-10", "150"}, {"2026-07-10", "200"}} {
		if _, err := fixture.service.Create(context.Background(), fixture.owner, Draft{Kind: Spending, Visibility: policy.Shared, Label: "Groceries", Category: "Groceries", Date: entry.date, AmountText: entry.amount, Provenance: Provenance{SourceID: source.ID, SourceFamily: source.Family, SourceVersion: source.Version, LocatorKind: "row", LocatorValue: string(rune('2' + index))}}); err != nil {
			t.Fatal(err)
		}
	}
	asOf := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	for _, want := range []struct {
		months int
		first  string
		values []string
	}{{3, "2026-05", []string{"0", "150", "200"}}, {6, "2026-02", []string{"0", "0", "100", "0", "150", "200"}}, {12, "2025-08", []string{"0", "0", "0", "0", "0", "0", "0", "0", "100", "0", "150", "200"}}} {
		summary, err := fixture.service.SummarizeRange(context.Background(), fixture.owner, AllRecords, asOf, want.months)
		if err != nil || len(summary.Trends) != 1 {
			t.Fatalf("range %d summary = %#v, %v", want.months, summary.Trends, err)
		}
		trend := summary.Trends[0]
		if len(trend.Months) != want.months || trend.Months[0].Month.Format("2006-01") != want.first || len(trend.Trendline) != want.months {
			t.Fatalf("range %d trend boundary = %#v", want.months, trend)
		}
		for index, value := range trend.Months {
			if got := value.Value.PlainString(); got != want.values[index] {
				t.Fatalf("range %d month %d = %s, want %s", want.months, index, got, want.values[index])
			}
		}
		if trend.Previous.PlainString() != "150" || trend.Current.PlainString() != "200" || trend.Change.PlainString() != "50" {
			t.Fatalf("range %d change = %#v", want.months, trend)
		}
		if trend.Trendline[len(trend.Trendline)-1] <= trend.Trendline[0] {
			t.Fatalf("range %d trendline = %#v", want.months, trend.Trendline)
		}
	}
}

func deepEqualJSON(left, right any) bool { return reflect.DeepEqual(left, right) }
