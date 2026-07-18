package app

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/glnarayanan/mithra/internal/health"
	"github.com/glnarayanan/mithra/internal/policy"
)

type HealthView struct {
	Navigation   []NavigationItem
	CSRF         string
	Scope        string
	HasRecords   bool
	Status       string
	Error        string
	Series       []HealthSeriesView
	Conflicts    []HealthConflictView
	Appointments []HealthAppointmentView
	Routines     []HealthRoutineView
	Records      []HealthRecordView
}

type HealthSeriesView struct{ ID, Subject, Analyte, LatestValue, Unit, LatestDate, ReferenceRange, AccessibleSummary, Points, EvidenceURL string }
type HealthConflictView struct{ RecordID, Version, Analyte, Reason, EvidenceURL string }
type HealthAppointmentView struct{ Label, Subject, Location, DateISO, DateLabel, EvidenceURL string }
type HealthRoutineView struct{ Label, Subject, Schedule, EvidenceURL string }
type HealthRecordView struct{ Label, Subject, Date, Observed, Context, Scope, EvidenceURL string }

func (a *App) healthLens(w http.ResponseWriter, r *http.Request) {
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
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		return
	}
	filter := health.ScopeFilter(r.URL.Query().Get("scope"))
	if filter != health.SharedRecords && filter != health.PersonalRecords {
		filter = health.AllRecords
	}
	summary, err := a.healthRecords.Summarize(r.Context(), scope, filter)
	if err != nil {
		logRequestError(a.logger, r.Context(), "health_query_failed")
		a.renderHealth(r.Context(), w, HealthView{Navigation: navigationForPath("/health"), CSRF: csrf, Scope: string(filter), Error: "Your records were not changed. Try this view again."})
		return
	}
	view := healthView(summary, filter, csrf, time.Now())
	if r.URL.Query().Get("corrected") == "1" {
		view.Status = "The corrected value and unit are now active. The original source remains linked."
	}
	a.renderHealth(r.Context(), w, view)
}

func (a *App) correctHealthObservation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowedFor(w, "POST")
		return
	}
	scope, _, ok := a.authenticated(r)
	if !ok {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	if !a.validSessionMutation(r, a.sessionCookie(r)) {
		writeError(w, http.StatusForbidden, "request verification failed")
		return
	}
	id := strings.TrimSpace(r.PostForm.Get("record_id"))
	value := strings.TrimSpace(r.PostForm.Get("value"))
	unit := strings.TrimSpace(r.PostForm.Get("unit"))
	version, err := strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
	if err != nil || version < 1 || len(id) > 128 || len(value) > 128 || len(unit) > 64 {
		writeError(w, http.StatusBadRequest, "correct value and unit are required")
		return
	}
	if _, err := a.healthRecords.CorrectObservation(r.Context(), scope, id, version, value, unit); err != nil {
		writeError(w, http.StatusConflict, "the observation changed; reload and try again")
		return
	}
	http.Redirect(w, r, "/health?corrected=1", http.StatusSeeOther)
}

func (a *App) renderHealth(ctx context.Context, w http.ResponseWriter, view HealthView) {
	if view.Navigation == nil {
		view.Navigation = navigationForPath("/health")
	}
	if view.Scope == "" {
		view.Scope = string(health.AllRecords)
	}
	rendered := newBufferedResponse(maxResponseBodyBytes)
	if err := a.templates.ExecuteTemplate(rendered, "health.html", view); err != nil || rendered.overflow {
		logRequestError(a.logger, ctx, "health_render_failed")
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	rendered.commit(w)
}

func healthView(summary health.Summary, filter health.ScopeFilter, csrf string, now time.Time) HealthView {
	view := HealthView{Navigation: navigationForPath("/health"), CSRF: csrf, Scope: string(filter), HasRecords: len(summary.Observations)+len(summary.Appointments)+len(summary.Routines) > 0}
	for index, series := range summary.Series {
		latest := series.Observations[len(series.Observations)-1]
		date, _ := time.Parse("2006-01-02", latest.ObservedOn)
		reference := ""
		if latest.ReferenceLow != nil || latest.ReferenceHigh != nil {
			low, high := "…", "…"
			if latest.ReferenceLow != nil {
				low = latest.ReferenceLow.PlainString()
			}
			if latest.ReferenceHigh != nil {
				high = latest.ReferenceHigh.PlainString()
			}
			reference = low + "–" + high + " " + latest.ReferenceUnit
		}
		parts := make([]string, 0, len(series.Observations))
		for _, item := range series.Observations {
			parts = append(parts, item.ObservedOn+" "+item.Value.PlainString()+" "+series.Unit)
		}
		view.Series = append(view.Series, HealthSeriesView{ID: strconv.Itoa(index + 1), Subject: series.Subject, Analyte: series.Analyte, LatestValue: latest.Value.PlainString(), Unit: series.Unit, LatestDate: date.Format("2 Jan 2006"), ReferenceRange: strings.TrimSpace(reference), AccessibleSummary: strings.Join(parts, "; ") + ".", Points: healthPoints(series.Observations), EvidenceURL: healthSourceURL(latest.SourceID)})
	}
	for _, conflict := range summary.Conflicts {
		view.Conflicts = append(view.Conflicts, HealthConflictView{RecordID: conflict.RecordID, Version: strconv.FormatInt(conflict.Version, 10), Analyte: conflict.Analyte, Reason: conflict.Reason, EvidenceURL: healthSourceURL(conflict.SourceID)})
	}
	today := now.Format("2006-01-02")
	for _, appointment := range summary.Appointments {
		if appointment.ScheduledOn < today {
			continue
		}
		date, _ := time.Parse("2006-01-02", appointment.ScheduledOn)
		view.Appointments = append(view.Appointments, HealthAppointmentView{Label: appointment.Label, Subject: appointment.Subject, Location: appointment.Location, DateISO: appointment.ScheduledOn, DateLabel: date.Format("2 Jan 2006"), EvidenceURL: healthSourceURL(appointment.SourceID)})
	}
	for _, routine := range summary.Routines {
		view.Routines = append(view.Routines, HealthRoutineView{Label: routine.Label, Subject: routine.Subject, Schedule: routine.Cadence + " · next recorded " + routine.NextDueOn, EvidenceURL: healthSourceURL(routine.SourceID)})
	}
	for _, observation := range summary.Observations {
		date, _ := time.Parse("2006-01-02", observation.ObservedOn)
		context := joinPresent(observation.Specimen, observation.Method, observation.ReferenceContext)
		scope := "Shared"
		if observation.Visibility == policy.Personal {
			scope = "Only you"
		}
		view.Records = append(view.Records, HealthRecordView{Label: observation.Analyte, Subject: observation.Subject, Date: date.Format("2 Jan 2006"), Observed: observation.OriginalValue + " " + observation.Unit, Context: context, Scope: scope, EvidenceURL: healthSourceURL(observation.SourceID)})
	}
	return view
}

func healthPoints(observations []health.Observation) string {
	if len(observations) == 1 {
		return "110,32"
	}
	values := make([]float64, len(observations))
	minimum, maximum := math.MaxFloat64, -math.MaxFloat64
	for index, item := range observations {
		value, _ := strconv.ParseFloat(item.Value.PlainString(), 64)
		values[index] = value
		if value < minimum {
			minimum = value
		}
		if value > maximum {
			maximum = value
		}
	}
	span := maximum - minimum
	if span == 0 {
		span = 1
	}
	points := make([]string, len(values))
	for index, value := range values {
		x := 12 + float64(index)*196/float64(len(values)-1)
		y := 54 - (value-minimum)*44/span
		points[index] = fmt.Sprintf("%.1f,%.1f", x, y)
	}
	return strings.Join(points, " ")
}
func joinPresent(values ...string) string {
	var result []string
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, strings.TrimSpace(value))
		}
	}
	if len(result) == 0 {
		return "Not supplied"
	}
	return strings.Join(result, " · ")
}
func healthSourceURL(id string) string { return "/sources/" + url.PathEscape(id) }
