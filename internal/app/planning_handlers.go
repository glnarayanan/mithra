package app

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/glnarayanan/mithra/internal/health"
	"github.com/glnarayanan/mithra/internal/planning"
)

type PlanningView struct {
	Navigation    []NavigationItem
	CSRF          string
	View          string
	Timezone      string
	Status        string
	Error         string
	PeriodLabel   string
	PreviousURL   string
	TodayURL      string
	NextURL       string
	MonthURL      string
	WeekURL       string
	AgendaURL     string
	WeekdayLabels []string
	CalendarDays  []CalendarDayView
	Agenda        []PlanningEventView
	Plans         []PlanningPlanView
}

type CalendarDayView struct {
	DayNumber      int
	AccessibleDate string
	FocusURL       string
	InPeriod       bool
	IsToday        bool
	Events         []PlanningEventView
}

type PlanningEventView struct {
	ID         string
	Title      string
	Owner      string
	Location   string
	DateTime   string
	EndDate    string
	DateLabel  string
	TimeLabel  string
	Conflict   string
	SourceURL  string
	ICSURL     string
	GoogleURL  string
	AgendaURL  string
	Exportable bool
}

type PlanningPlanView struct {
	Goal          string
	GoalSourceURL string
	Title         string
	SourceURL     string
	Owner         string
	Constraint    string
	Milestones    []PlanningMilestoneView
}

type PlanningMilestoneView struct {
	Title      string
	State      string
	DueISO     string
	DueLabel   string
	Dependency string
	SourceURL  string
}

func (a *App) planningLens(w http.ResponseWriter, r *http.Request) {
	if !allowsRead(r.Method) {
		methodNotAllowed(w)
		return
	}
	scope, csrf, ok := a.authenticated(r)
	if !ok {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodHead {
		writeHTMLHead(w)
		return
	}
	zone, err := a.planningRecords.GetTimezone(r.Context(), scope)
	if err != nil {
		a.renderPlanning(r.Context(), w, PlanningView{Navigation: navigationForPath("/planning"), CSRF: csrf, Error: "The calendar is temporarily unavailable."})
		return
	}
	focus := planningFocusDate(r.URL.Query().Get("date"), zone, time.Now())
	viewName := r.URL.Query().Get("view")
	if viewName != "week" && viewName != "agenda" {
		viewName = "month"
	}
	from, to, gridFrom, gridTo := planningRange(viewName, focus)
	events, err := a.planningRecords.Events(r.Context(), scope, planning.AllRecords, gridFrom.Format("2006-01-02"), gridTo.Format("2006-01-02"))
	if err != nil {
		a.renderPlanning(r.Context(), w, PlanningView{Navigation: navigationForPath("/planning"), CSRF: csrf, View: viewName, Timezone: zone, Error: "The calendar could not be loaded. Try again."})
		return
	}
	conflicts, err := a.planningRecords.Conflicts(r.Context(), scope, planning.AllRecords, gridFrom.Format("2006-01-02"), gridTo.Format("2006-01-02"))
	if err != nil {
		a.renderPlanning(r.Context(), w, PlanningView{Navigation: navigationForPath("/planning"), CSRF: csrf, View: viewName, Timezone: zone, Error: "Schedule conflicts could not be checked. Try again."})
		return
	}
	plans, err := a.planningRecords.Plans(r.Context(), scope, planning.AllRecords)
	if err != nil {
		a.renderPlanning(r.Context(), w, PlanningView{Navigation: navigationForPath("/planning"), CSRF: csrf, View: viewName, Timezone: zone, Error: "Plans could not be loaded. Try again."})
		return
	}
	healthSummary, err := a.healthRecords.Summarize(r.Context(), scope, health.AllRecords)
	if err != nil {
		a.renderPlanning(r.Context(), w, PlanningView{Navigation: navigationForPath("/planning"), CSRF: csrf, View: viewName, Timezone: zone, Error: "Dated health records could not be loaded. Try again."})
		return
	}
	conflictByID := map[string]string{}
	for _, conflict := range conflicts {
		conflictByID[conflict.First.ID] = conflict.Reason
		conflictByID[conflict.Second.ID] = conflict.Reason
	}
	view := PlanningView{Navigation: navigationForPath("/planning"), CSRF: csrf, View: viewName, Timezone: zone, PeriodLabel: planningPeriodLabel(viewName, from, to), WeekdayLabels: []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}}
	view.PreviousURL, view.TodayURL, view.NextURL, view.MonthURL, view.WeekURL, view.AgendaURL = planningURLs(viewName, focus, zone)
	healthEvents := healthPlanningEvents(healthSummary, zone)
	calendarEvents := planningEventViews(append(events, planningEventsInRange(healthEvents, gridFrom, gridTo)...), conflictByID, zone)
	view.Agenda = planningEventViews(append(planningEventsInRange(events, from, to), planningEventsInRange(healthEvents, from, to)...), conflictByID, zone)
	view.CalendarDays = planningDays(calendarEvents, gridFrom, gridTo, from, to, time.Now())
	view.Plans = planningPlanViews(plans)
	if r.URL.Query().Get("export") == "unavailable" {
		view.Status = "Complete the event date and timezone before exporting it."
	}
	a.renderPlanning(r.Context(), w, view)
}

func (a *App) planningICS(w http.ResponseWriter, r *http.Request) {
	if !allowsRead(r.Method) {
		methodNotAllowed(w)
		return
	}
	scope, _, ok := a.authenticated(r)
	if !ok {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/planning/events/"), ".ics")
	if id == "" || strings.Contains(id, "/") || len(id) > 128 || !strings.HasSuffix(r.URL.Path, ".ics") {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	zone, err := a.planningRecords.GetTimezone(r.Context(), scope)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	event, err := a.planningRecords.GetEvent(r.Context(), scope, id)
	if strings.HasPrefix(id, "health-") {
		summary, summaryErr := a.healthRecords.Summarize(r.Context(), scope, health.AllRecords)
		if summaryErr == nil {
			for _, candidate := range healthPlanningEvents(summary, zone) {
				if candidate.ID == id {
					event, err = candidate, nil
					break
				}
			}
		}
	}
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	calendar, err := planning.ICS(event, zone)
	if err != nil {
		http.Redirect(w, r, "/planning?export=unavailable", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="mithra-event.ics"`)
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write([]byte(calendar))
}

func healthPlanningEvents(summary health.Summary, zone string) []planning.Event {
	events := make([]planning.Event, 0, len(summary.Appointments)+len(summary.Routines))
	for _, appointment := range summary.Appointments {
		if appointment.Status != "planned" {
			continue
		}
		event := planning.Event{ID: "health-appointment-" + appointment.ID, Title: appointment.Label, Location: appointment.Location, AllDay: true, StartsOn: appointment.ScheduledOn, EndsOn: appointment.ScheduledOn, Status: "planned", SourceID: appointment.SourceID, OwnerIDs: []string{appointment.OwnerID}}
		if start, err := time.Parse("2006-01-02T15:04", appointment.ScheduledAt); err == nil {
			event.AllDay = false
			event.StartsOn, event.EndsOn = "", ""
			event.StartsAt = start.Format("2006-01-02T15:04")
			event.EndsAt = start.Add(30 * time.Minute).Format("2006-01-02T15:04")
			event.Timezone = zone
		}
		events = append(events, event)
	}
	for _, routine := range summary.Routines {
		if routine.Status != "active" {
			continue
		}
		events = append(events, planning.Event{ID: "health-routine-" + routine.ID, Title: routine.Label, AllDay: true, StartsOn: routine.NextDueOn, EndsOn: routine.NextDueOn, Status: "planned", SourceID: routine.SourceID, OwnerIDs: []string{routine.OwnerID}})
	}
	return events
}

func (a *App) renderPlanning(ctx context.Context, w http.ResponseWriter, view PlanningView) {
	if view.Navigation == nil {
		view.Navigation = navigationForPath("/planning")
	}
	a.renderTemplate(ctx, w, "planning.html", view)
}

func planningFocusDate(raw, zone string, now time.Time) time.Time {
	if parsed, err := time.Parse("2006-01-02", raw); err == nil {
		return parsed
	}
	location := time.UTC
	if zone != "" {
		if parsed, err := time.LoadLocation(zone); err == nil {
			location = parsed
		}
	}
	local := now.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
}

func planningRange(view string, focus time.Time) (time.Time, time.Time, time.Time, time.Time) {
	if view == "week" {
		from := focus.AddDate(0, 0, -((int(focus.Weekday()) + 6) % 7))
		to := from.AddDate(0, 0, 6)
		return from, to, from, to
	}
	from := time.Date(focus.Year(), focus.Month(), 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, -1)
	gridFrom := from.AddDate(0, 0, -((int(from.Weekday()) + 6) % 7))
	gridTo := to.AddDate(0, 0, 6-((int(to.Weekday())+6)%7))
	return from, to, gridFrom, gridTo
}

func planningPeriodLabel(view string, from, to time.Time) string {
	if view == "week" {
		if from.Year() != to.Year() {
			return fmt.Sprintf("%d %s %d–%d %s %d", from.Day(), from.Month(), from.Year(), to.Day(), to.Month(), to.Year())
		}
		if from.Month() == to.Month() {
			return fmt.Sprintf("%d–%d %s %d", from.Day(), to.Day(), from.Month(), from.Year())
		}
		return fmt.Sprintf("%d %s–%d %s %d", from.Day(), from.Month(), to.Day(), to.Month(), to.Year())
	}
	label := from.Format("January 2006")
	if view == "agenda" {
		label += " agenda"
	}
	return label
}

func planningURLs(view string, focus time.Time, zone string) (string, string, string, string, string, string) {
	shift := func(deltaMonth, deltaDay int) string {
		return planningURL(view, focus.AddDate(0, deltaMonth, deltaDay))
	}
	previous, next := shift(-1, 0), shift(1, 0)
	if view == "week" {
		previous, next = shift(0, -7), shift(0, 7)
	}
	today := planningURL(view, planningFocusDate("", zone, time.Now()))
	return previous, today, next, planningURL("month", focus), planningURL("week", focus), planningURL("agenda", focus)
}

func planningURL(view string, date time.Time) string {
	query := url.Values{"view": {view}, "date": {date.Format("2006-01-02")}}
	return "/planning?" + query.Encode()
}

func planningEventViews(events []planning.Event, conflicts map[string]string, zone string) []PlanningEventView {
	views := make([]PlanningEventView, 0, len(events))
	for _, event := range events {
		start, _ := planningEventBounds(event)
		_, finish := planningEventBounds(event)
		view := PlanningEventView{ID: event.ID, Title: event.Title, Location: event.Location, DateTime: planningEventDateTime(event), EndDate: finish.Format("2006-01-02"), DateLabel: planningDateLabel(event), TimeLabel: planningTimeLabel(event), Conflict: conflicts[event.ID], SourceURL: "/sources/" + url.PathEscape(event.SourceID), ICSURL: "/planning/events/" + url.PathEscape(event.ID) + ".ics", AgendaURL: planningURL("agenda", start) + "#event-" + url.PathEscape(event.ID)}
		if len(event.OwnerIDs) == 1 {
			view.Owner = "one household adult"
		} else if len(event.OwnerIDs) > 1 {
			view.Owner = strconv.Itoa(len(event.OwnerIDs)) + " household adults"
		}
		if google, err := planning.GoogleCalendarURL(event, zone); err == nil {
			view.GoogleURL = google
			view.Exportable = true
		}
		if _, err := planning.ICS(event, zone); err != nil {
			view.Exportable = false
		}
		view.DateTime = start.Format("2006-01-02")
		views = append(views, view)
	}
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].DateTime == views[j].DateTime {
			return views[i].TimeLabel < views[j].TimeLabel
		}
		return views[i].DateTime < views[j].DateTime
	})
	return views
}

func planningEventsInRange(events []planning.Event, from, to time.Time) []planning.Event {
	filtered := make([]planning.Event, 0, len(events))
	for _, event := range events {
		start, end := planningEventBounds(event)
		if !end.Before(from) && !start.After(to) {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func planningDays(events []PlanningEventView, from, to, periodFrom, periodTo, now time.Time) []CalendarDayView {
	byDate := map[string][]PlanningEventView{}
	for _, event := range events {
		start, _ := time.Parse("2006-01-02", event.DateTime)
		end, err := time.Parse("2006-01-02", event.EndDate)
		if err != nil || end.Before(start) {
			end = start
		}
		for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
			byDate[day.Format("2006-01-02")] = append(byDate[day.Format("2006-01-02")], event)
		}
	}
	var days []CalendarDayView
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		eventsForDay := byDate[day.Format("2006-01-02")]
		entry := CalendarDayView{DayNumber: day.Day(), AccessibleDate: day.Format("Monday, 2 January 2006"), FocusURL: planningURL("agenda", day), InPeriod: !day.Before(periodFrom) && !day.After(periodTo), IsToday: sameDate(day, now), Events: eventsForDay}
		days = append(days, entry)
	}
	return days
}

func planningPlanViews(summary planning.PlanSummary) []PlanningPlanView {
	goals := map[string]planning.Goal{}
	for _, goal := range summary.Goals {
		goals[goal.ID] = goal
	}
	events := map[string]planning.Event{}
	for _, event := range summary.Events {
		events[event.ID] = event
	}
	milestones := map[string][]planning.Milestone{}
	for _, milestone := range summary.Milestones {
		milestones[milestone.PlanID] = append(milestones[milestone.PlanID], milestone)
	}
	var out []PlanningPlanView
	for _, plan := range summary.Plans {
		goal := goals[plan.GoalID]
		view := PlanningPlanView{Goal: goal.Title, GoalSourceURL: planningSourceURL(goal.SourceID), Title: plan.Title, SourceURL: planningSourceURL(plan.SourceID)}
		constraintSet := map[string]struct{}{}
		dependencies := map[string]string{}
		for _, event := range summary.Events {
			if event.PlanID != plan.ID {
				continue
			}
			for _, constraint := range event.Constraints {
				constraintSet[constraint.Kind+": "+constraint.Value] = struct{}{}
			}
			if event.MilestoneID != "" && len(event.DependsOn) > 0 {
				dependencies[event.MilestoneID] = events[event.DependsOn[0]].Title
			}
		}
		var constraints []string
		for constraint := range constraintSet {
			constraints = append(constraints, constraint)
		}
		sort.Strings(constraints)
		view.Constraint = strings.Join(constraints, "; ")
		for _, milestone := range milestones[plan.ID] {
			entry := PlanningMilestoneView{Title: milestone.Title, State: milestone.Status, DueISO: milestone.DueOn, Dependency: dependencies[milestone.ID], SourceURL: planningSourceURL(milestone.SourceID)}
			if date, err := time.Parse("2006-01-02", milestone.DueOn); err == nil {
				entry.DueLabel = date.Format("2 Jan 2006")
			}
			view.Milestones = append(view.Milestones, entry)
		}
		out = append(out, view)
	}
	return out
}

func planningSourceURL(sourceID string) string {
	if sourceID == "" {
		return ""
	}
	return "/sources/" + url.PathEscape(sourceID)
}

func planningEventBounds(event planning.Event) (time.Time, time.Time) {
	if event.AllDay {
		start, _ := time.Parse("2006-01-02", event.StartsOn)
		end := event.EndsOn
		if end == "" {
			end = event.StartsOn
		}
		finish, _ := time.Parse("2006-01-02", end)
		return start, finish
	}
	start, _ := time.Parse("2006-01-02T15:04", event.StartsAt)
	end, _ := time.Parse("2006-01-02T15:04", event.EndsAt)
	return start, end
}

func planningEventDateTime(event planning.Event) string {
	if event.AllDay {
		return event.StartsOn
	}
	return event.StartsAt
}

func planningDateLabel(event planning.Event) string {
	start, end := planningEventBounds(event)
	if !sameDate(start, end) {
		return start.Format("2 Jan 2006") + "–" + end.Format("2 Jan 2006")
	}
	return start.Format("2 Jan 2006")
}

func planningTimeLabel(event planning.Event) string {
	if event.AllDay {
		return "All day"
	}
	start, end := planningEventBounds(event)
	return start.Format("15:04") + "–" + end.Format("15:04")
}

func sameDate(a, b time.Time) bool {
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day()
}
