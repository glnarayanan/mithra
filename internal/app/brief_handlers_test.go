package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/coaching"
	"github.com/glnarayanan/mithra/internal/finance"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/storage"
)

func TestFamilyBriefLoadsWithoutAIThenRefreshesEvidenceAndKeepsPartnerPrivateOut(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com", "partner@example.com")
	ownerSession := activate(t, application, mailer, "owner@example.com", "owner secure password", nil)
	owner := ownerScope(t, application, ownerSession)
	invite, err := application.auth.CreateInvitation(context.Background(), owner, "partner@example.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	partnerSession := activateInvitation(t, application, "partner secure password", bootstrapInvitation(t, application, invite.Token))
	partner := ownerScope(t, application, partnerSession)

	sharedSource := coachingTestSource(t, application, owner, policy.Shared, "shared salary")
	if _, err := application.finance.Create(context.Background(), owner, finance.Draft{Kind: finance.Income, Visibility: policy.Shared, Label: "Salary", Category: "Income", IncompleteNote: "amount needs correction", Provenance: coachingFinanceProvenance(sharedSource)}); err != nil {
		t.Fatal(err)
	}
	partnerSource := coachingTestSource(t, application, partner, policy.Personal, "partner secret")
	if _, err := application.finance.Create(context.Background(), partner, finance.Draft{Kind: finance.Spending, Visibility: policy.Personal, Label: "Partner private purchase", Category: "Private", Date: "2026-07-18", AmountText: "99", Provenance: coachingFinanceProvenance(partnerSource)}); err != nil {
		t.Fatal(err)
	}

	providerCalls := 0
	if err := application.providerSettings.ReplaceOpenAI(context.Background(), owner, "sk-coaching-test-secret", func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	application.openAIClient = &http.Client{Transport: appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		providerCalls++
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		input, _ := payload["input"].(string)
		if strings.Contains(input, "Partner private purchase") || strings.Contains(input, "partner secret") {
			t.Fatalf("shared prompt leaked partner private data: %s", input)
		}
		var contextPayload struct {
			Scope string `json:"scope"`
			Facts []struct {
				EvidenceID string `json:"evidence_id"`
			} `json:"facts"`
		}
		if json.Unmarshal([]byte(input), &contextPayload) != nil || contextPayload.Scope != "shared" || len(contextPayload.Facts) != 1 {
			t.Fatalf("context payload = %s", input)
		}
		id := contextPayload.Facts[0].EvidenceID
		output := `{"lead":{"title":"Salary needs a source correction","copy":"Salary is recorded with a value still to confirm.","when":"","evidence_ids":["` + id + `"]},"changes":[],"dates":[],"inconsistencies":[{"title":"Salary value","copy":"The recorded amount still needs confirmation.","when":"","evidence_ids":["` + id + `"]}],"priorities":[{"title":"Confirm salary value","copy":"Check the value against its source.","when":"","evidence_ids":["` + id + `"]}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(captureProviderBody(output))), Request: request}, nil
	})}

	initial := serve(application, coachingGET("/", ownerSession))
	for _, required := range []string{"Family Brief", "Here’s what changed and what’s coming up.", "At a glance", "Worth checking", "Regenerate AI insights"} {
		if !strings.Contains(initial.Body.String(), required) {
			t.Fatalf("initial brief missing %q: %s", required, initial.Body.String())
		}
	}
	for _, retired := range []string{"The household, calmly in view.", "Bring in the first household fact", "Mithra is showing only facts and evidence visible to you.", "Live application view"} {
		if strings.Contains(initial.Body.String(), retired) {
			t.Fatalf("initial brief retained internal copy %q", retired)
		}
	}
	if initial.Code != http.StatusOK || providerCalls != 0 || !strings.Contains(initial.Body.String(), "Salary") || strings.Contains(initial.Body.String(), "Partner private purchase") {
		t.Fatalf("initial brief code=%d calls=%d body=%q", initial.Code, providerCalls, initial.Body.String())
	}
	mailBefore := len(mailer.messages)
	refreshed := serve(application, coachingPOST("/brief/refresh", ownerSession, url.Values{}))
	if refreshed.Code != http.StatusOK || providerCalls != 1 || !strings.Contains(refreshed.Body.String(), "Salary needs a source correction") {
		t.Fatalf("refresh code=%d calls=%d body=%q", refreshed.Code, providerCalls, refreshed.Body.String())
	}
	if len(mailer.messages) != mailBefore+1 || strings.Contains(mailer.last(t).Text, "Salary") || !strings.Contains(mailer.last(t).Text, "Open Mithra") {
		t.Fatalf("nudge email = %#v", mailer.last(t))
	}
	cached := serve(application, coachingGET("/", ownerSession))
	if cached.Code != http.StatusOK || providerCalls != 1 {
		t.Fatalf("cached page code=%d calls=%d", cached.Code, providerCalls)
	}
	var nudgeID string
	if err := application.db.QueryRow(`SELECT id FROM coaching_nudges WHERE owner_user_id=?`, owner.ActorID).Scan(&nudgeID); err != nil {
		t.Fatal(err)
	}
	preference := serve(application, coachingPOST("/notifications/nudge", ownerSession, url.Values{"nudge_id": {nudgeID}, "nudge_action": {"enable-follow-up"}}))
	if preference.Code != http.StatusOK || !strings.Contains(preference.Body.String(), "Reminder preference updated") {
		t.Fatalf("follow-up preference=%d %q", preference.Code, preference.Body.String())
	}
	mailBeforeFollowUp := len(mailer.messages)
	followUp := serve(application, coachingPOST("/brief/refresh", ownerSession, url.Values{}))
	if followUp.Code != http.StatusOK || providerCalls != 2 || len(mailer.messages) != mailBeforeFollowUp+1 || strings.Contains(mailer.last(t).Text, "Salary") {
		t.Fatalf("follow-up code=%d calls=%d mail=%#v", followUp.Code, providerCalls, mailer.last(t))
	}
	week := serve(application, coachingGET("/review", ownerSession))
	if week.Code != http.StatusOK || !strings.Contains(week.Body.String(), "Week in Review") || !strings.Contains(week.Body.String(), "What changed") || !strings.Contains(week.Body.String(), "Needs a look") || !strings.Contains(week.Body.String(), "Only you") || !strings.Contains(week.Body.String(), "Learn about privacy") || !strings.Contains(week.Body.String(), "Regenerate AI insights") {
		t.Fatalf("week=%d %q", week.Code, week.Body.String())
	}
}

func TestBriefOnboardingUsesProviderStateAndHouseholdRole(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com", "partner@example.com")
	ownerSession := activate(t, application, mailer, "owner@example.com", "owner secure password", nil)
	owner := ownerScope(t, application, ownerSession)

	ownerBrief := serve(application, coachingGET("/", ownerSession))
	if ownerBrief.Code != http.StatusOK || !strings.Contains(ownerBrief.Body.String(), "Connect a model provider to begin") || !strings.Contains(ownerBrief.Body.String(), `href="/settings#provider-title"`) {
		t.Fatalf("owner onboarding = %d %q", ownerBrief.Code, ownerBrief.Body.String())
	}

	invite, err := application.auth.CreateInvitation(context.Background(), owner, "partner@example.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	partnerSession := activateInvitation(t, application, "partner secure password", bootstrapInvitation(t, application, invite.Token))
	partnerBrief := serve(application, coachingGET("/", partnerSession))
	if partnerBrief.Code != http.StatusOK || !strings.Contains(partnerBrief.Body.String(), "Ask your household owner to connect one") || strings.Contains(partnerBrief.Body.String(), `href="/settings#provider-title"`) {
		t.Fatalf("partner onboarding = %d %q", partnerBrief.Code, partnerBrief.Body.String())
	}

	if err := application.providerSettings.ReplaceOpenAI(context.Background(), owner, "sk-onboarding-test-secret", func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	configured := serve(application, coachingGET("/", partnerSession))
	for _, required := range []string{"Add your first update", `href="#capture"`, `href="/imports"`} {
		if configured.Code != http.StatusOK || !strings.Contains(configured.Body.String(), required) {
			t.Fatalf("configured onboarding missing %q: %d %q", required, configured.Code, configured.Body.String())
		}
	}
}

func TestCoachingRefreshRejectsUnsupportedEvidence(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	session := activate(t, application, mailer, "owner@example.com", "owner secure password", nil)
	scope := ownerScope(t, application, session)
	source := coachingTestSource(t, application, scope, policy.Shared, "shared")
	if _, err := application.finance.Create(context.Background(), scope, finance.Draft{Kind: finance.Asset, Visibility: policy.Shared, Label: "Savings", Category: "Asset", Date: "2026-07-18", AmountText: "100", Provenance: coachingFinanceProvenance(source)}); err != nil {
		t.Fatal(err)
	}
	if err := application.providerSettings.ReplaceOpenAI(context.Background(), scope, "sk-coaching-test-secret", func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	application.openAIClient = &http.Client{Transport: appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		output := `{"lead":{"title":"Invented","copy":"Unsupported claim.","when":"","evidence_ids":["missing"]},"changes":[],"dates":[],"inconsistencies":[],"priorities":[]}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(captureProviderBody(output))), Request: request}, nil
	})}
	response := serve(application, coachingPOST("/brief/refresh", session, url.Values{}))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "could not refresh") || strings.Contains(response.Body.String(), "Unsupported claim") {
		t.Fatalf("unsupported refresh=%d %q", response.Code, response.Body.String())
	}
}

func TestPrivateItemsDoNotRepeatOneFactAcrossSections(t *testing.T) {
	item := coaching.Item{Title: "Private fact", EvidenceIDs: []string{"evidence-1"}}
	items := privateItems(coaching.Narrative{
		Changes:         []coaching.Item{item},
		Dates:           []coaching.Item{item},
		Inconsistencies: []coaching.Item{item},
	})
	if len(items) != 1 || items[0].Title != item.Title {
		t.Fatalf("private items = %#v", items)
	}
}

func coachingTestSource(t *testing.T, application *App, scope policy.ActorScope, visibility policy.Visibility, content string) storage.Source {
	t.Helper()
	source, err := application.sources.Store(context.Background(), scope, []byte(content), storage.Metadata{Family: "text", Version: 1, Visibility: visibility, LocatorKind: "source", LocatorValue: "update"})
	if err != nil {
		t.Fatal(err)
	}
	return source
}
func coachingFinanceProvenance(source storage.Source) finance.Provenance {
	return finance.Provenance{SourceID: source.ID, SourceFamily: source.Family, SourceVersion: source.Version, LocatorKind: "source", LocatorValue: "update", GeneratedBy: "application", SchemaVersion: "finance-v1"}
}
func coachingGET(path string, session browserSession) *http.Request {
	return authForm(http.MethodGet, path, url.Values{}, []*http.Cookie{session.session, session.csrf})
}
func coachingPOST(path string, session browserSession, values url.Values) *http.Request {
	values.Set("csrf", session.csrf.Value)
	return authForm(http.MethodPost, path, values, []*http.Cookie{session.session, session.csrf})
}
