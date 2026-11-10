package app

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/people"
	"github.com/glnarayanan/mithra/internal/policy"
)

func TestTypedHealthEntriesReachHealthAndThePersonPage(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	scope := ownerScope(t, application, session)
	child, err := application.people.Create(context.Background(), scope, people.Draft{Name: "Meera", Relationship: "child", BirthDate: "2026-03-14"})
	if err != nil {
		t.Fatal(err)
	}
	personPath := "/family/" + child.ID
	post := func(values url.Values) string {
		response := serve(application, authenticatedHealthRequest(session, http.MethodPost, "/health/entries", values))
		if response.Code != http.StatusSeeOther {
			t.Fatalf("entry %v status=%d %q", values, response.Code, response.Body.String())
		}
		return response.Header().Get("Location")
	}
	today := time.Now().UTC().Format("2006-01-02")
	if location := post(url.Values{"action": {"measurement"}, "return": {personPath}, "subject": {"Meera"}, "analyte": {"body weight"}, "value": {"6.4"}, "observed_on": {today}, "visibility": {"shared"}}); location != personPath+"?entry=measurement" {
		t.Fatalf("measurement redirect=%q", location)
	}
	post(url.Values{"action": {"medicine_add"}, "return": {personPath}, "subject": {"Meera"}, "name": {"Vitamin D drops"}, "dosage": {"400 IU daily"}, "visibility": {"shared"}})
	post(url.Values{"action": {"note_add"}, "return": {personPath}, "subject": {"Meera"}, "content": {"Next check in two months."}, "visibility": {"personal"}})
	post(url.Values{"action": {"habit_add"}, "return": {personPath}, "subject": {"Meera"}, "name": {"Tummy time"}, "kind": {"number"}, "unit": {"min"}, "visibility": {"shared"}})
	if bad := post(url.Values{"action": {"measurement"}, "return": {"https://example.com"}, "subject": {"Meera"}, "analyte": {"Weight"}, "value": {"heavy"}}); bad != "/health?entry=failed" {
		t.Fatalf("bad entry redirect=%q", bad)
	}
	page := serve(application, authenticatedHealthRequest(session, http.MethodGet, personPath+"?entry=measurement", nil))
	body := page.Body.String()
	for _, want := range []string{"<h1>Meera</h1>", "Measurement added.", "Weight", "6.4<small>kg</small>", "Vitamin D drops", "400 IU daily", "Next check in two months.", "Tummy time", "0 of the last 7 days"} {
		if !strings.Contains(body, want) {
			t.Fatalf("person page missing %q", want)
		}
	}
	healthPage := serve(application, authenticatedHealthRequest(session, http.MethodGet, "/health", nil)).Body.String()
	for _, want := range []string{"Vitamin D drops", "health-tests", "Fasting Glucose"} {
		if !strings.Contains(healthPage, want) {
			t.Fatalf("health page missing %q", want)
		}
	}
	observations, _ := application.healthRecords.Summarize(context.Background(), scope, "all")
	if len(observations.Observations) != 1 || observations.Observations[0].Analyte != "Weight" || observations.Observations[0].Unit != "kg" || observations.Observations[0].Visibility != policy.Shared {
		t.Fatalf("typed observation=%#v", observations.Observations)
	}
	if _, _, err := application.sources.Read(context.Background(), scope, observations.Observations[0].SourceID); err != nil {
		t.Fatalf("typed measurement has no readable source: %v", err)
	}
}
