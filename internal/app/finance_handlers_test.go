package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/glnarayanan/mithra/internal/finance"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/storage"
)

func TestFinanceLensRendersEmptyPartialAndExactScopedStates(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com", "partner@example.com")
	ownerSession := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	owner := ownerScope(t, application, ownerSession)

	empty := serve(application, authenticatedFinanceRequest(ownerSession, "/finance"))
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), "<h1>Finance</h1>") || !strings.Contains(empty.Body.String(), "No finance records yet") {
		t.Fatalf("empty finance = %d %q", empty.Code, empty.Body.String())
	}

	sharedSource, err := application.sources.Store(context.Background(), owner, []byte("shared finance evidence"), storage.Metadata{Family: "text", Version: 1, Visibility: policy.Shared, LocatorKind: "source", LocatorValue: "capture-1"})
	if err != nil {
		t.Fatal(err)
	}
	personalSource, err := application.sources.Store(context.Background(), owner, []byte("personal finance evidence"), storage.Metadata{Family: "text", Version: 1, Visibility: policy.Personal, LocatorKind: "source", LocatorValue: "capture-2"})
	if err != nil {
		t.Fatal(err)
	}
	create := func(source storage.Source, visibility policy.Visibility, label, amount string) finance.Record {
		record, err := application.finance.Create(context.Background(), owner, finance.Draft{Kind: finance.Spending, Visibility: visibility, Label: label, Category: "Household", Date: "2026-07-10", AmountText: amount, Provenance: finance.Provenance{SourceID: source.ID, SourceFamily: source.Family, SourceVersion: source.Version, LocatorKind: "source", LocatorValue: source.LocatorValue}})
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	shared := create(sharedSource, policy.Shared, "Shared groceries", "125.50")
	create(sharedSource, policy.Shared, "Unreadable receipt", "twelve")
	create(personalSource, policy.Personal, "Private purchase", "75")

	ownerAll := serve(application, authenticatedFinanceRequest(ownerSession, "/finance"))
	body := ownerAll.Body.String()
	for _, text := range []string{"Shared groceries", "Unreadable receipt", "200.50", "1 need a value", "amount needs correction", "/sources/" + shared.SourceID} {
		if !strings.Contains(body, text) {
			t.Fatalf("owner finance missing %q: %s", text, body)
		}
	}
	if !strings.Contains(body, `name="amount" value="twelve"`) || strings.Contains(body, "$125") {
		t.Fatalf("finance correction omitted the current value or rendered a currency symbol: %s", body)
	}

	invite := serve(application, settingsPost(ownerSession, "partner@example.com"))
	if invite.Code != http.StatusOK {
		t.Fatalf("invite = %d", invite.Code)
	}
	partnerToken := tokenFromMessage(t, mailer.last(t), "token")
	partnerSession := activateInvitation(t, application, "a partner secure password", bootstrapInvitation(t, application, partnerToken))
	partner := serve(application, authenticatedFinanceRequest(partnerSession, "/finance?scope=all"))
	if partner.Code != http.StatusOK || strings.Contains(partner.Body.String(), "200.50") || !strings.Contains(partner.Body.String(), "125.50") {
		t.Fatalf("partner finance scope = %d %q", partner.Code, partner.Body.String())
	}

	sharedEvidence := serve(application, authenticatedFinanceRequest(partnerSession, "/sources/"+sharedSource.ID))
	if sharedEvidence.Code != http.StatusOK || sharedEvidence.Body.String() != "shared finance evidence" {
		t.Fatalf("shared evidence = %d %q", sharedEvidence.Code, sharedEvidence.Body.String())
	}
	privateEvidence := serve(application, authenticatedFinanceRequest(partnerSession, "/sources/"+personalSource.ID))
	if privateEvidence.Code != http.StatusNotFound || strings.Contains(privateEvidence.Body.String(), "personal") {
		t.Fatalf("private evidence = %d %q", privateEvidence.Code, privateEvidence.Body.String())
	}
}

func TestFinanceLensKeepsErrorsGenericAndEscaped(t *testing.T) {
	application := newTestApp(t)
	response := httptest.NewRecorder()
	application.renderFinance(context.Background(), response, FinanceView{Scope: "all", Error: `<script>alert("private")</script>`})
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), `<script>alert`) || !strings.Contains(response.Body.String(), "&lt;script&gt;") {
		t.Fatalf("error finance = %d %q", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "No finance records yet") {
		t.Fatalf("fatal error also rendered empty state: %q", response.Body.String())
	}
}

func TestFinanceTrendRangeDefaultsAndRendersServerLinks(t *testing.T) {
	for raw, want := range map[string]int{"": 3, "3": 3, "6": 6, "12": 12, "2": 3, "six": 3} {
		if got := financeTrendRange(raw); got != want {
			t.Fatalf("range %q = %d, want %d", raw, got, want)
		}
	}
	application, mailer := newAuthTestApp(t, "owner@example.com")
	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	owner := ownerScope(t, application, session)
	source, err := application.sources.Store(context.Background(), owner, []byte("spending"), storage.Metadata{Family: "text", Version: 1, Visibility: policy.Personal, LocatorKind: "source", LocatorValue: "capture"})
	if err != nil {
		t.Fatal(err)
	}
	for _, month := range []string{"2026-05-10", "2026-06-10", "2026-07-10"} {
		if _, err := application.finance.Create(context.Background(), owner, finance.Draft{Kind: finance.Spending, Visibility: policy.Personal, Label: "Groceries", Category: "Groceries", Date: month, AmountText: "10", Provenance: finance.Provenance{SourceID: source.ID, SourceFamily: source.Family, SourceVersion: source.Version, LocatorKind: "source", LocatorValue: source.LocatorValue}}); err != nil {
			t.Fatal(err)
		}
	}
	page := serve(application, authenticatedFinanceRequest(session, "/finance?scope=personal&range=6"))
	for _, text := range []string{`href="/finance?scope=personal&amp;range=3"`, `href="/finance?scope=personal&amp;range=6" aria-current="page"`, `class="analytics-chart"`, `May 2026`, `A zero point does not prove that a record was entered that month.`} {
		if !strings.Contains(page.Body.String(), text) {
			t.Fatalf("finance range missing %q: %s", text, page.Body.String())
		}
	}
}

func TestFinanceAnalyticsEscapesTrendContent(t *testing.T) {
	application := newTestApp(t)
	response := httptest.NewRecorder()
	application.renderFinance(context.Background(), response, FinanceView{Scope: "all", HasRecords: true, Trends: []FinanceTrendView{{ID: "1", Category: `<script>alert("private")</script>`, PeriodLabel: "May 2026 to July 2026", StartLabel: "May 2026", EndLabel: "July 2026", StartValue: "0", EndValue: "10", ChangeText: "+10", AccessibleSummary: "May 2026 0; July 2026 10.", Points: "16,68 224,16", TrendlinePoints: "16,68 224,16", Markers: []FinanceChartMarkerView{{X: "16", Y: "68", Label: "May 2026", Value: "0", Zero: true}}}}})
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), `<script>alert`) || !strings.Contains(response.Body.String(), "&lt;script&gt;") {
		t.Fatalf("trend content was not escaped: %d %q", response.Code, response.Body.String())
	}
}

func TestFinanceIssueCanBeCorrectedFromTheLens(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	owner := ownerScope(t, application, session)
	source, err := application.sources.Store(context.Background(), owner, []byte("receipt"), storage.Metadata{Family: "text", Version: 1, Visibility: policy.Personal, LocatorKind: "source", LocatorValue: "capture"})
	if err != nil {
		t.Fatal(err)
	}
	record, err := application.finance.Create(context.Background(), owner, finance.Draft{Kind: finance.Spending, Visibility: policy.Personal, Label: "Household purchase", Category: "Household", Date: "20 July", AmountText: "125", Provenance: finance.Provenance{SourceID: source.ID, SourceFamily: source.Family, SourceVersion: source.Version, LocatorKind: "source", LocatorValue: source.LocatorValue}})
	if err != nil {
		t.Fatal(err)
	}
	page := serve(application, authenticatedFinanceRequest(session, "/finance"))
	if !strings.Contains(page.Body.String(), `action="/finance/correct"`) || !strings.Contains(page.Body.String(), `type="date"`) || strings.Contains(page.Body.String(), `value="20 July"`) {
		t.Fatalf("correction form missing: %q", page.Body.String())
	}
	values := url.Values{"csrf": {session.csrf.Value}, "record_id": {record.ID}, "version": {"1"}, "kind": {"spending"}, "date": {"2026-07-20"}, "amount": {"125"}}
	corrected := serve(application, authForm(http.MethodPost, "/finance/correct", values, []*http.Cookie{session.session, session.csrf}))
	if corrected.Code != http.StatusSeeOther || corrected.Header().Get("Location") != "/finance?corrected=1" {
		t.Fatalf("correction response = %d %q", corrected.Code, corrected.Body.String())
	}
	var date, reason, sourceID string
	if err := application.db.QueryRow(`SELECT spent_on,incomplete_reason,source_id FROM finance_spending WHERE active=1 AND supersedes_id=?`, record.ID).Scan(&date, &reason, &sourceID); err != nil || date != "2026-07-20" || reason != "" || sourceID != source.ID {
		t.Fatalf("corrected record date=%q reason=%q source=%q err=%v", date, reason, sourceID, err)
	}
}

func TestFinanceCategoryCanBeCorrectedWithoutReenteringTheRecord(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	owner := ownerScope(t, application, session)
	source, err := application.sources.Store(context.Background(), owner, []byte("finance source"), storage.Metadata{Family: "text", Version: 1, Visibility: policy.Personal, LocatorKind: "source", LocatorValue: "capture"})
	if err != nil {
		t.Fatal(err)
	}
	record, err := application.finance.Create(context.Background(), owner, finance.Draft{Kind: finance.Spending, Visibility: policy.Personal, Label: "Monthly loan payment", Category: "Expenses", Date: "2026-07-20", AmountText: "21000", Provenance: finance.Provenance{SourceID: source.ID, SourceFamily: source.Family, SourceVersion: source.Version, LocatorKind: "source", LocatorValue: source.LocatorValue}})
	if err != nil {
		t.Fatal(err)
	}
	page := serve(application, authenticatedFinanceRequest(session, "/finance"))
	if !strings.Contains(page.Body.String(), `class="finance-category-form"`) || !strings.Contains(page.Body.String(), `value="Expenses"`) || !strings.Contains(page.Body.String(), `value="Loan repayment"`) {
		t.Fatalf("category editor missing: %q", page.Body.String())
	}
	values := url.Values{"csrf": {session.csrf.Value}, "record_id": {record.ID}, "version": {"1"}, "kind": {"spending"}, "category": {"Loan repayment"}}
	corrected := serve(application, authForm(http.MethodPost, "/finance/correct", values, []*http.Cookie{session.session, session.csrf}))
	if corrected.Code != http.StatusSeeOther || corrected.Header().Get("Location") != "/finance?corrected=1" {
		t.Fatalf("category correction = %d %q", corrected.Code, corrected.Body.String())
	}
	var category, date, amount string
	if err := application.db.QueryRow(`SELECT category,spent_on,amount_original FROM finance_spending WHERE active=1 AND supersedes_id=?`, record.ID).Scan(&category, &date, &amount); err != nil || category != "Loan repayment" || date != "2026-07-20" || amount != "21000" {
		t.Fatalf("corrected category=%q date=%q amount=%q err=%v", category, date, amount, err)
	}
}

func TestBudgetEndDateCanBeCorrectedFromTheLens(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	owner := ownerScope(t, application, session)
	source, err := application.sources.Store(context.Background(), owner, []byte("budget"), storage.Metadata{Family: "text", Version: 1, Visibility: policy.Personal, LocatorKind: "source", LocatorValue: "capture"})
	if err != nil {
		t.Fatal(err)
	}
	record, err := application.finance.Create(context.Background(), owner, finance.Draft{Kind: finance.Budget, Visibility: policy.Personal, Label: "Household budget", Category: "Household", Date: "2026-07-01", EndDate: "end of July", AmountText: "1000", Provenance: finance.Provenance{SourceID: source.ID, SourceFamily: source.Family, SourceVersion: source.Version, LocatorKind: "source", LocatorValue: source.LocatorValue}})
	if err != nil {
		t.Fatal(err)
	}
	page := serve(application, authenticatedFinanceRequest(session, "/finance"))
	if !strings.Contains(page.Body.String(), `name="end_date"`) || strings.Contains(page.Body.String(), `value="end of July"`) {
		t.Fatalf("budget correction form missing a safe end date input: %q", page.Body.String())
	}
	values := url.Values{"csrf": {session.csrf.Value}, "record_id": {record.ID}, "version": {"1"}, "kind": {"budget"}, "date": {"2026-07-01"}, "end_date": {"2026-07-31"}, "amount": {"1000"}}
	corrected := serve(application, authForm(http.MethodPost, "/finance/correct", values, []*http.Cookie{session.session, session.csrf}))
	if corrected.Code != http.StatusSeeOther || corrected.Header().Get("Location") != "/finance?corrected=1" {
		t.Fatalf("budget correction response = %d %q", corrected.Code, corrected.Body.String())
	}
	var endDate, reason string
	if err := application.db.QueryRow(`SELECT ends_on,incomplete_reason FROM finance_budgets WHERE active=1 AND supersedes_id=?`, record.ID).Scan(&endDate, &reason); err != nil || endDate != "2026-07-31" || reason != "" {
		t.Fatalf("corrected budget end=%q reason=%q err=%v", endDate, reason, err)
	}
}

func authenticatedFinanceRequest(session browserSession, target string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.AddCookie(session.session)
	request.AddCookie(session.csrf)
	return request
}
