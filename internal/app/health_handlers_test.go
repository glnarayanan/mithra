package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/glnarayanan/mithra/internal/health"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/storage"
)

func TestHealthLensRendersFactualSeriesConflictDatesAndCorrection(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	scope := ownerScope(t, application, session)
	source, err := application.sources.Store(context.Background(), scope, []byte("health report evidence"), storage.Metadata{Family: "text", Version: 1, Visibility: policy.Shared, LocatorKind: "source", LocatorValue: "report-1"})
	if err != nil {
		t.Fatal(err)
	}
	p := health.Provenance{SourceID: source.ID, SourceFamily: source.Family, SourceVersion: source.Version, LocatorKind: "source", LocatorValue: source.LocatorValue}
	create := func(value, unit, date string) health.Observation {
		record, err := application.healthRecords.CreateObservation(context.Background(), scope, health.ObservationDraft{Visibility: policy.Shared, Subject: "Alex", Analyte: "Glucose", Specimen: "serum", Method: "lab method", ReferenceContext: "report range", ObservedOn: date, Value: value, Unit: unit, ReferenceLow: "70", ReferenceHigh: "110", ReferenceUnit: "mg/dL", Provenance: p})
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	create("1.00", "g/L", "2026-06-01")
	create("105", "mg/dL", "2026-07-01")
	conflict := func(value, unit, date string) health.Observation {
		record, err := application.healthRecords.CreateObservation(context.Background(), scope, health.ObservationDraft{Visibility: policy.Shared, Subject: "Alex", Analyte: "Potassium", Specimen: "serum", Method: "ise", ReferenceContext: "report range", ObservedOn: date, Value: value, Unit: unit, Provenance: p})
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	conflict("4.2", "mmol/L", "2026-06-01")
	wrong := conflict("160", "mg/dL", "2026-07-01")
	if _, err := application.healthRecords.CreateAppointment(context.Background(), scope, health.AppointmentDraft{Visibility: policy.Shared, Subject: "Alex", Label: "Annual check-up", Location: "Clinic", ScheduledOn: "2026-07-25", Provenance: p}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.healthRecords.CreateRoutine(context.Background(), scope, health.RoutineDraft{Visibility: policy.Shared, Subject: "Alex", Label: "Recorded routine", Cadence: "Every morning", NextDueOn: "2026-07-19", Provenance: p}); err != nil {
		t.Fatal(err)
	}
	page := serve(application, authenticatedHealthRequest(session, http.MethodGet, "/health", nil))
	body := page.Body.String()
	for _, required := range []string{"Health trends", "For your records, not medical advice.", "does not diagnose", "Glucose", "105", "mg/dL", "Values kept separate", "Correct value", "Help with corrections", "Annual check-up", "Recorded routine", "View latest source"} {
		if !strings.Contains(body, required) {
			t.Fatalf("health lens missing %q: %s", required, body)
		}
	}
	for _, forbidden := range []string{"you should take", "diagnosis:", "recommended treatment"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("health lens contains advice-shaped wording %q", forbidden)
		}
	}
	corrected := serve(application, authenticatedHealthRequest(session, http.MethodPost, "/health/correct", url.Values{"record_id": {wrong.ID}, "version": {"1"}, "value": {"4.3"}, "unit": {"mmol/L"}}))
	if corrected.Code != http.StatusSeeOther || corrected.Header().Get("Location") != "/health?corrected=1" {
		t.Fatalf("correction=%d %q", corrected.Code, corrected.Body.String())
	}
	after := serve(application, authenticatedHealthRequest(session, http.MethodGet, "/health?corrected=1", nil))
	if !strings.Contains(after.Body.String(), "corrected value and unit are now active") || strings.Contains(after.Body.String(), "Values kept separate") {
		t.Fatalf("corrected health=%s", after.Body.String())
	}
}

func TestHealthLensEmptyAndCSRFBoundary(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	empty := serve(application, authenticatedHealthRequest(session, http.MethodGet, "/health?scope=personal", nil))
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), "No health records yet") {
		t.Fatalf("empty health=%d %q", empty.Code, empty.Body.String())
	}
	bad := httptest.NewRequest(http.MethodPost, "/health/correct", strings.NewReader(url.Values{"record_id": {"x"}, "version": {"1"}, "value": {"1"}, "unit": {"kg"}}.Encode()))
	bad.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	bad.Header.Set("Origin", testOrigin)
	bad.AddCookie(session.session)
	bad.AddCookie(session.csrf)
	response := serve(application, bad)
	if response.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d", response.Code)
	}
}

func authenticatedHealthRequest(session browserSession, method, target string, values url.Values) *http.Request {
	if values == nil {
		request := httptest.NewRequest(method, target, nil)
		request.AddCookie(session.session)
		request.AddCookie(session.csrf)
		return request
	}
	values.Set("csrf", session.csrf.Value)
	return authForm(method, target, values, []*http.Cookie{session.session, session.csrf})
}
