package app

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
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
	// Person narrows every list to one subject; People offers the switch.
	Person          string
	People          []HealthRangeView
	ScopeOptions    []HealthRangeView
	NextAppointment *HealthAppointmentView
	// ShowSubject is true when the lists mix more than one person.
	ShowSubject bool
	Groups      []HealthGroupView
	Care        CareView
	Catalog     []HealthCatalogOption
	Subjects    []string
}

type HealthRangeView struct {
	Label, URL string
	Current    bool
}
type HealthSeriesView struct {
	ID, Subject, Analyte, FirstValue, FirstDate, LatestValue, Unit, LatestDate, ReferenceRange, AccessibleSummary, Points, Trendline, Change, Direction, EvidenceURL string
	Markers                                                                                                                                                          []HealthChartMarkerView
	HasTrend                                                                                                                                                         bool
	RangeStatus                                                                                                                                                      string
	RangeOut                                                                                                                                                         bool
	Readings                                                                                                                                                         int
	// Note says when a young child's baby log holds more weights than Health.
	Note string
}
type HealthChartMarkerView struct{ X, Y, Label, Value string }
type HealthConflictView struct{ RecordID, Version, Analyte, Reason, EvidenceURL string }
type HealthAppointmentView struct{ Label, Subject, Location, DateISO, DateLabel, Relative, EvidenceURL string }
type HealthRoutineView struct{ Label, Subject, Schedule, EvidenceURL string }
type HealthRecordView struct {
	Label, Subject, Date, DateISO, Observed, Context, Scope, EvidenceURL string
	Private                                                              bool
}

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
	now := a.householdNow(r.Context(), scope)
	view := healthViewFor(summary, filter, csrf, now, rangeValue, r.URL.Query().Get("person"))
	view.Catalog = healthCatalogOptions()
	if status := healthEntryStatus(r.URL.Query().Get("entry")); status != "" {
		view.Status = status
	}
	if list, err := a.people.List(r.Context(), scope); err == nil {
		for _, person := range list {
			view.Subjects = appendUnique(view.Subjects, person.Name)
		}
		a.noteBabyWeights(r.Context(), scope, list, now, &view)
	}
	if log, err := a.healthRecords.CareLogFor(r.Context(), scope, view.Person, now.AddDate(0, 0, -6).Format("2006-01-02")); err == nil {
		view.Care = careView(log, now, view.Person == "")
		if view.Care.Medicines != nil || view.Care.Habits != nil || view.Care.Notes != nil {
			view.HasRecords = true
		}
	}
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
	if view.Groups == nil {
		view.Groups = groupSeries(view.Series)
	}
	if view.ScopeOptions == nil {
		for _, option := range []struct{ value, label string }{{string(health.AllRecords), "Everything"}, {string(health.SharedRecords), "Shared"}, {string(health.PersonalRecords), "Only you"}} {
			view.ScopeOptions = append(view.ScopeOptions, HealthRangeView{Label: option.label, URL: healthURL(view.Range, option.value, view.Person), Current: option.value == view.Scope})
		}
	}
	rendered := newBufferedResponse(maxResponseBodyBytes)
	if err := a.templates.ExecuteTemplate(rendered, "health.html", view); err != nil || rendered.overflow {
		logRequestError(a.logger, ctx, "health_render_failed")
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	a.localize(ctx, rendered)
	rendered.commit(w)
}

func healthView(summary health.Summary, filter health.ScopeFilter, csrf string, now time.Time, rangeValue string) HealthView {
	return healthViewFor(summary, filter, csrf, now, rangeValue, "")
}

// healthViewFor builds the Health page, narrowed to one person when person
// names a subject found in the records.
func healthViewFor(summary health.Summary, filter health.ScopeFilter, csrf string, now time.Time, rangeValue, person string) HealthView {
	rangeValue = healthRange(rangeValue)
	people := healthSubjects(summary)
	person = strings.TrimSpace(person)
	known := false
	for _, subject := range people {
		if subject == person {
			known = true
		}
	}
	if !known {
		person = ""
	}
	if person != "" {
		summary = summaryForSubject(summary, person)
	}
	view := HealthView{Navigation: navigationForPath("/health"), CSRF: csrf, Scope: string(filter), Range: rangeValue, Person: person, HasRecords: len(summary.Observations)+len(summary.Appointments)+len(summary.Routines) > 0}
	view.Ranges = make([]HealthRangeView, 0, 4)
	for _, option := range []struct{ value, label string }{{"3", "3 months"}, {"6", "6 months"}, {"12", "12 months"}, {"all", "All time"}} {
		view.Ranges = append(view.Ranges, HealthRangeView{Label: option.label, URL: healthURL(option.value, string(filter), person), Current: option.value == rangeValue})
	}
	for _, option := range []struct{ value, label string }{{string(health.AllRecords), "Everything"}, {string(health.SharedRecords), "Shared"}, {string(health.PersonalRecords), "Only you"}} {
		view.ScopeOptions = append(view.ScopeOptions, HealthRangeView{Label: option.label, URL: healthURL(rangeValue, option.value, person), Current: option.value == string(filter)})
	}
	view.ShowSubject = len(people) > 1 && person == ""
	if len(people) > 1 {
		view.People = append(view.People, HealthRangeView{Label: "Everyone", URL: healthURL(rangeValue, string(filter), ""), Current: person == ""})
		for _, subject := range people {
			view.People = append(view.People, HealthRangeView{Label: subject, URL: healthURL(rangeValue, string(filter), subject), Current: subject == person})
		}
	}
	seriesForRange := summary.Series
	if rangeValue != "all" {
		seriesForRange = health.SeriesSince(summary.Series, now.AddDate(0, -healthRangeMonths(rangeValue), 0))
	}
	for index, series := range seriesForRange {
		first := series.Observations[0]
		latest := series.Observations[len(series.Observations)-1]
		firstDate, _ := time.Parse("2006-01-02", first.ObservedOn)
		date, _ := time.Parse("2006-01-02", latest.ObservedOn)
		reference := healthReference(latest)
		rangeStatus, rangeOut := healthRangeStatus(latest, series.Unit)
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
		view.Series = append(view.Series, HealthSeriesView{ID: strconv.Itoa(index + 1), Subject: series.Subject, Analyte: series.Analyte, FirstValue: first.Value.PlainString(), FirstDate: shortDate(firstDate), LatestValue: latest.Value.PlainString(), Unit: series.Unit, LatestDate: shortDate(date), ReferenceRange: strings.TrimSpace(reference), AccessibleSummary: summary, Points: points, Trendline: trendline, Markers: markers, Change: trend.Change, Direction: trend.Direction, HasTrend: len(series.Observations) > 1, EvidenceURL: healthSourceURL(latest.SourceID), RangeStatus: rangeStatus, RangeOut: rangeOut, Readings: len(series.Observations)})
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
		view.Appointments = append(view.Appointments, HealthAppointmentView{Label: appointment.Label, Subject: appointment.Subject, Location: appointment.Location, DateISO: appointment.ScheduledOn, DateLabel: humanDate(date), Relative: relativeDays(date, now), EvidenceURL: healthSourceURL(appointment.SourceID)})
	}
	sort.SliceStable(view.Appointments, func(i, j int) bool { return view.Appointments[i].DateISO < view.Appointments[j].DateISO })
	if len(view.Appointments) > 0 {
		next := view.Appointments[0]
		view.NextAppointment = &next
	}
	for _, routine := range summary.Routines {
		view.Routines = append(view.Routines, HealthRoutineView{Label: routine.Label, Subject: routine.Subject, Schedule: routineSchedule(routine), EvidenceURL: healthSourceURL(routine.SourceID)})
	}
	for _, observation := range summary.Observations {
		date, _ := time.Parse("2006-01-02", observation.ObservedOn)
		context := joinPresent(observation.Specimen, observation.Method, observation.ReferenceContext)
		scope := "Shared"
		if observation.Visibility == policy.Personal {
			scope = "Only you"
		}
		view.Records = append(view.Records, HealthRecordView{Label: observation.Analyte, Subject: observation.Subject, Date: humanDate(date), DateISO: observation.ObservedOn, Observed: strings.TrimSpace(observation.OriginalValue + " " + observation.Unit), Context: context, Scope: scope, Private: observation.Visibility == policy.Personal, EvidenceURL: healthSourceURL(observation.SourceID)})
	}
	sort.SliceStable(view.Records, func(i, j int) bool { return view.Records[i].DateISO > view.Records[j].DateISO })
	view.Groups = groupSeries(view.Series)
	view.Subjects = people
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
		result = append(result, HealthRangeView{Label: option.label, URL: healthURL(option.value, scope, ""), Current: option.value == rangeValue})
	}
	return result
}

func appendUnique(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

func healthURL(rangeValue, scope, person string) string {
	query := url.Values{"range": {rangeValue}}
	if scope != "" && scope != string(health.AllRecords) {
		query.Set("scope", scope)
	}
	if person != "" {
		query.Set("person", person)
	}
	return "/health?" + query.Encode()
}

// healthSubjects lists the people named in the records, in first-seen order.
func healthSubjects(summary health.Summary) []string {
	seen := map[string]bool{}
	var subjects []string
	add := func(subject string) {
		subject = strings.TrimSpace(subject)
		if subject != "" && !seen[subject] {
			seen[subject] = true
			subjects = append(subjects, subject)
		}
	}
	for _, series := range summary.Series {
		add(series.Subject)
	}
	for _, observation := range summary.Observations {
		add(observation.Subject)
	}
	for _, appointment := range summary.Appointments {
		add(appointment.Subject)
	}
	for _, routine := range summary.Routines {
		add(routine.Subject)
	}
	return subjects
}

func summaryForSubject(summary health.Summary, subject string) health.Summary {
	result := health.Summary{Conflicts: summary.Conflicts}
	for _, series := range summary.Series {
		if strings.TrimSpace(series.Subject) == subject {
			result.Series = append(result.Series, series)
		}
	}
	for _, observation := range summary.Observations {
		if strings.TrimSpace(observation.Subject) == subject {
			result.Observations = append(result.Observations, observation)
		}
	}
	for _, appointment := range summary.Appointments {
		if strings.TrimSpace(appointment.Subject) == subject {
			result.Appointments = append(result.Appointments, appointment)
		}
	}
	for _, routine := range summary.Routines {
		if strings.TrimSpace(routine.Subject) == subject {
			result.Routines = append(result.Routines, routine)
		}
	}
	return result
}

// healthReference writes the lab's reported range as plain words.
func healthReference(latest health.Observation) string {
	unit := strings.TrimSpace(latest.ReferenceUnit)
	switch {
	case latest.ReferenceLow != nil && latest.ReferenceHigh != nil:
		return strings.TrimSpace("Lab range " + latest.ReferenceLow.PlainString() + " to " + latest.ReferenceHigh.PlainString() + " " + unit)
	case latest.ReferenceLow != nil:
		return strings.TrimSpace("Lab range from " + latest.ReferenceLow.PlainString() + " " + unit)
	case latest.ReferenceHigh != nil:
		return strings.TrimSpace("Lab range up to " + latest.ReferenceHigh.PlainString() + " " + unit)
	}
	return ""
}

// healthRangeStatus compares the latest value with the lab's own range, only
// when both use the same unit. It states position, never meaning.
func healthRangeStatus(latest health.Observation, unit string) (string, bool) {
	if latest.ReferenceLow == nil && latest.ReferenceHigh == nil {
		return "", false
	}
	if reference := strings.TrimSpace(latest.ReferenceUnit); reference != "" && !strings.EqualFold(reference, strings.TrimSpace(unit)) {
		return "", false
	}
	value, err := strconv.ParseFloat(latest.Value.PlainString(), 64)
	if err != nil {
		return "", false
	}
	if latest.ReferenceLow != nil {
		if low, err := strconv.ParseFloat(latest.ReferenceLow.PlainString(), 64); err == nil && value < low {
			return "Below lab range", true
		}
	}
	if latest.ReferenceHigh != nil {
		if high, err := strconv.ParseFloat(latest.ReferenceHigh.PlainString(), 64); err == nil && value > high {
			return "Above lab range", true
		}
	}
	return "In lab range", false
}

func routineSchedule(routine health.Routine) string {
	schedule := strings.TrimSpace(routine.Cadence)
	if date, ok := parseISODate(routine.NextDueOn); ok {
		next := "next recorded " + humanDate(date)
		if schedule == "" {
			return next
		}
		return schedule + ", " + next
	}
	if schedule == "" && routine.NextDueOn != "" {
		return "next recorded " + routine.NextDueOn
	}
	return schedule
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
	return strings.Join(result, ", ")
}
func healthSourceURL(id string) string { return "/sources/" + url.PathEscape(id) }
