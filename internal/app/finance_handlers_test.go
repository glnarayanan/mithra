package app

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), "Nothing to total yet") {
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
	for _, text := range []string{"Shared groceries", "Unreadable receipt", "200.50", "1 incomplete excluded", "amount needs correction", "/sources/" + shared.SourceID} {
		if !strings.Contains(body, text) {
			t.Fatalf("owner finance missing %q: %s", text, body)
		}
	}
	if strings.Contains(body, "twelve") || strings.Contains(body, "$125") {
		t.Fatalf("finance rendered source text or a currency symbol: %s", body)
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
}

func authenticatedFinanceRequest(session browserSession, target string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.AddCookie(session.session)
	request.AddCookie(session.csrf)
	return request
}
