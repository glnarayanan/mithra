package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/planning"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/storage"
)

func TestPlanningLensCalendarViewsConflictsAndExports(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	scope := ownerScope(t, application, session)
	if err := application.planningRecords.SetTimezone(context.Background(), scope, "Asia/Kolkata"); err != nil {
		t.Fatal(err)
	}
	source, err := application.sources.Store(context.Background(), scope, []byte("planning evidence"), storage.Metadata{Family: "text", Version: 1, Visibility: policy.Shared, LocatorKind: "source", LocatorValue: "plan-1"})
	if err != nil {
		t.Fatal(err)
	}
	provenance := planning.Provenance{SourceID: source.ID, SourceFamily: source.Family, SourceVersion: source.Version, LocatorKind: source.LocatorKind, LocatorValue: source.LocatorValue, GeneratedBy: "application", SchemaVersion: "planning-v1"}
	goalID, err := application.planningRecords.CreateGoal(context.Background(), scope, planning.GoalDraft{Visibility: policy.Shared, Title: "Make the school transition calm", TargetOn: "2026-08-01", Provenance: provenance})
	if err != nil {
		t.Fatal(err)
	}
	planID, err := application.planningRecords.CreatePlan(context.Background(), scope, planning.PlanDraft{Visibility: policy.Shared, GoalID: goalID, Title: "School transition", Provenance: provenance})
	if err != nil {
		t.Fatal(err)
	}
	milestoneID, err := application.planningRecords.CreateMilestone(context.Background(), scope, planning.MilestoneDraft{Visibility: policy.Shared, PlanID: planID, Title: "Meet the teacher", DueOn: "2026-07-20", Provenance: provenance})
	if err != nil {
		t.Fatal(err)
	}
	create := func(draft planning.EventDraft) planning.Event {
		draft.Visibility = policy.Shared
		draft.Provenance = provenance
		event, err := application.planningRecords.CreateEvent(context.Background(), scope, draft)
		if err != nil {
			t.Fatal(err)
		}
		return event
	}
	trip := create(planning.EventDraft{Title: "Family trip", Description: "Review the confirmed booking details", Location: "Pondicherry", AllDay: true, StartsOn: "2026-07-24", EndsOn: "2026-07-26", OwnerIDs: []string{scope.ActorID}})
	create(planning.EventDraft{PlanID: planID, MilestoneID: milestoneID, Title: "School conversation", StartsAt: "2026-07-20T10:00", EndsAt: "2026-07-20T11:00", Timezone: "Asia/Kolkata", OwnerIDs: []string{scope.ActorID}, DependsOn: []string{trip.ID}, Constraints: []planning.Constraint{{Kind: "availability", Value: "after the family trip"}}})
	create(planning.EventDraft{Title: "Home contractor", StartsAt: "2026-07-20T10:30", EndsAt: "2026-07-20T11:30", Timezone: "Asia/Kolkata", OwnerIDs: []string{scope.ActorID}})
	create(planning.EventDraft{Title: "June spill", AllDay: true, StartsOn: "2026-06-29"})

	month := serve(application, authenticatedPlanningRequest(session, "/planning?view=month&date=2026-07-15"))
	if month.Code != http.StatusOK {
		t.Fatalf("month status=%d body=%s", month.Code, month.Body.String())
	}
	for _, required := range []string{"A bird’s-eye view.", "July 2026", "Family trip", "School conversation", "Home contractor", "Assigned owners have overlapping events", "Download .ics", "Open Google Calendar draft", "Later Mithra changes will not update", "Help with calendar exports", "calendar-mobile-agenda", "Owned plans", "School transition", "Make the school transition calm", "Meet the teacher", "availability: after the family trip", "After Family trip", "View goal source", "View plan source", "View milestone source"} {
		if !strings.Contains(month.Body.String(), required) {
			t.Fatalf("month missing %q: %s", required, month.Body.String())
		}
	}
	week := serve(application, authenticatedPlanningRequest(session, "/planning?view=week&date=2026-07-20"))
	if week.Code != http.StatusOK || !strings.Contains(week.Body.String(), "20–26 July 2026") || !strings.Contains(week.Body.String(), "Family trip") {
		t.Fatalf("week=%d %s", week.Code, week.Body.String())
	}
	agenda := serve(application, authenticatedPlanningRequest(session, "/planning?view=agenda&date=2026-07-15"))
	if agenda.Code != http.StatusOK || !strings.Contains(agenda.Body.String(), "July 2026 agenda") || strings.Contains(agenda.Body.String(), "calendar-wide-view") || strings.Contains(agenda.Body.String(), "June spill") {
		t.Fatalf("agenda=%d %s", agenda.Code, agenda.Body.String())
	}

	export := serve(application, authenticatedPlanningRequest(session, "/planning/events/"+trip.ID+".ics"))
	if export.Code != http.StatusOK || export.Header().Get("Content-Type") != "text/calendar; charset=utf-8" || !strings.Contains(export.Body.String(), "SUMMARY:Family trip\r\n") || !strings.Contains(export.Body.String(), "DTSTART;VALUE=DATE:20260724\r\n") || !strings.Contains(export.Body.String(), "DTEND;VALUE=DATE:20260727\r\n") {
		t.Fatalf("ics=%d %#v %q", export.Code, export.Header(), export.Body.String())
	}
}

func TestPlanningCalendarMathKeepsCrossYearAndMultiDayBoundaries(t *testing.T) {
	from := time.Date(2026, 12, 28, 0, 0, 0, 0, time.UTC)
	to := time.Date(2027, 1, 3, 0, 0, 0, 0, time.UTC)
	if label := planningPeriodLabel("week", from, to); label != "28 December 2026–3 January 2027" {
		t.Fatalf("cross-year label=%q", label)
	}
	event := PlanningEventView{ID: "trip", Title: "Trip", DateTime: "2026-12-30", EndDate: "2027-01-02"}
	days := planningDays([]PlanningEventView{event}, from, to, from, to, time.Time{})
	var visible int
	for _, day := range days {
		if len(day.Events) == 1 {
			visible++
		}
	}
	if visible != 4 {
		t.Fatalf("multi-day visible days=%d", visible)
	}
	if label := planningDateLabel(planning.Event{StartsAt: "2026-12-30T10:00", EndsAt: "2026-12-30T11:00"}); label != "30 Dec 2026" {
		t.Fatalf("same-day timed label=%q", label)
	}
}

func TestPlanningLensRequiresSessionAndSettingsConfirmsTimezone(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	unauthenticated := serve(application, httptest.NewRequest(http.MethodGet, "/planning", nil))
	if unauthenticated.Code != http.StatusSeeOther || unauthenticated.Header().Get("Location") != "/auth/login" {
		t.Fatalf("unauthenticated=%d %#v", unauthenticated.Code, unauthenticated.Header())
	}
	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	saved := serve(application, authenticatedSettingsRequest(session, http.MethodPost, url.Values{"action": {"save_timezone"}, "timezone": {"Asia/Kolkata"}}))
	if saved.Code != http.StatusOK || !strings.Contains(saved.Body.String(), "timezone confirmed as Asia/Kolkata") || !strings.Contains(saved.Body.String(), `value="Asia/Kolkata"`) {
		t.Fatalf("saved timezone=%d %s", saved.Code, saved.Body.String())
	}
	bad := serve(application, authenticatedSettingsRequest(session, http.MethodPost, url.Values{"action": {"save_timezone"}, "timezone": {"Mars/Olympus"}}))
	if bad.Code != http.StatusOK || !strings.Contains(bad.Body.String(), "valid timezone") {
		t.Fatalf("bad timezone=%d %s", bad.Code, bad.Body.String())
	}
}

func authenticatedPlanningRequest(session browserSession, target string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.AddCookie(session.session)
	request.AddCookie(session.csrf)
	return request
}
