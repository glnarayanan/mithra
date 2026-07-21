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
	Range        string
	Ranges       []HealthRangeView
	HasRecords   bool
	Status       string
	Error        string
	Series       []HealthSeriesView
	Conflicts    []HealthConflictView
	Appointments []HealthAppointmentView
	Routines     []HealthRoutineView
	Records      []HealthRecordView
}

type HealthRangeView struct {
	Label, URL string
	Current    bool
}
type HealthSeriesView struct {
	ID, Subject, Analyte, FirstValue, FirstDate, LatestValue, Unit, LatestDate, ReferenceRange, AccessibleSummary, Points, Trendline, Change, Direction, EvidenceURL string
	Markers                                                                                                                                                          []HealthChartMarkerView
	HasTrend                                                                                                                                                         bool
}
type HealthChartMarkerView struct{ X, Y, Label, Value string }
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
	rangeValue := healthRange(r.URL.Query().Get("range"))
	summary, err := a.healthRecords.Summarize(r.Context(), scope, filter)
	if err != nil {
		logRequestError(a.logger, r.Context(), "health_query_failed")
		a.renderHealth(r.Context(), w, HealthView{Navigation: navigationForPath("/health"), CSRF: csrf, Scope: string(filter), Range: rangeValue, Error: "Your records were not changed. Try this view again."})
		return
	}
	view := healthView(summary, filter, csrf, time.Now(), rangeValue)
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
	view.Range = healthRange(view.Range)
	if view.Ranges == nil {
		view.Ranges = healthRangeOptions(view.Range, view.Scope)
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

func healthView(summary health.Summary, filter health.ScopeFilter, csrf string, now time.Time, rangeValue string) HealthView {
	rangeValue = healthRange(rangeValue)
	view := HealthView{Navigation: navigationForPath("/health"), CSRF: csrf, Scope: string(filter), Range: rangeValue, Ranges: healthRangeOptions(rangeValue, string(filter)), HasRecords: len(summary.Observations)+len(summary.Appointments)+len(summary.Routines) > 0}
	seriesForRange := summary.Series
	if rangeValue != "all" {
		seriesForRange = health.SeriesSince(summary.Series, now.AddDate(0, -healthRangeMonths(rangeValue), 0))
	}
	for index, series := range seriesForRange {
		first := series.Observations[0]
		latest := series.Observations[len(series.Observations)-1]
		firstDate, _ := time.Parse("2006-01-02", first.ObservedOn)
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
		trend := health.SeriesTrend(series.Observations)
		points, trendline, markers := healthPoints(series.Observations, trend.Line)
		summary := strings.Join(parts, "; ") + "."
		if len(series.Observations) > 1 {
			summary += " Change " + trend.Change + " " + series.Unit + ", direction " + trend.Direction + ". A linear trendline is shown."
		}
		view.Series = append(view.Series, HealthSeriesView{ID: strconv.Itoa(index + 1), Subject: series.Subject, Analyte: series.Analyte, FirstValue: first.Value.PlainString(), FirstDate: firstDate.Format("2 Jan 2006"), LatestValue: latest.Value.PlainString(), Unit: series.Unit, LatestDate: date.Format("2 Jan 2006"), ReferenceRange: strings.TrimSpace(reference), AccessibleSummary: summary, Points: points, Trendline: trendline, Markers: markers, Change: trend.Change, Direction: trend.Direction, HasTrend: len(series.Observations) > 1, EvidenceURL: healthSourceURL(latest.SourceID)})
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

func healthRange(input string) string {
	switch input {
	case "6", "12", "all":
		return input
	default:
		return "3"
	}
}

func healthRangeMonths(rangeValue string) int {
	switch rangeValue {
	case "6":
		return 6
	case "12":
		return 12
	default:
		return 3
	}
}

func healthRangeOptions(rangeValue, scope string) []HealthRangeView {
	options := []struct{ value, label string }{{"3", "3 months"}, {"6", "6 months"}, {"12", "12 months"}, {"all", "All time"}}
	result := make([]HealthRangeView, 0, len(options))
	for _, option := range options {
		query := url.Values{"range": {option.value}}
		if scope != "" && scope != string(health.AllRecords) {
			query.Set("scope", scope)
		}
		result = append(result, HealthRangeView{Label: option.label, URL: "/health?" + query.Encode(), Current: option.value == rangeValue})
	}
	return result
}

func healthPoints(observations []health.Observation, trendline []float64) (string, string, []HealthChartMarkerView) {
	if len(observations) == 1 {
		return "110,42", "", []HealthChartMarkerView{{X: "110", Y: "42", Label: observations[0].ObservedOn, Value: observations[0].Value.PlainString()}}
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
	for _, value := range trendline {
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
	line := make([]string, len(trendline))
	markers := make([]HealthChartMarkerView, len(values))
	for index, value := range values {
		x := 16 + float64(index)*208/float64(len(values)-1)
		y := 68 - (value-minimum)*48/span
		points[index] = fmt.Sprintf("%.1f,%.1f", x, y)
		markers[index] = HealthChartMarkerView{X: fmt.Sprintf("%.1f", x), Y: fmt.Sprintf("%.1f", y), Label: observations[index].ObservedOn, Value: observations[index].Value.PlainString()}
	}
	for index, value := range trendline {
		x := 16 + float64(index)*208/float64(len(trendline)-1)
		y := 68 - (value-minimum)*48/span
		line[index] = fmt.Sprintf("%.1f,%.1f", x, y)
	}
	return strings.Join(points, " "), strings.Join(line, " "), markers
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
