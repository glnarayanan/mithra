package app

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/glnarayanan/mithra/internal/carelog"
	"github.com/glnarayanan/mithra/internal/health"
	"github.com/glnarayanan/mithra/internal/people"
	"github.com/glnarayanan/mithra/internal/policy"
)

// BabyLogView is the baby log on a child's page. It only restates what was
// logged: times, counts and amounts.
type BabyLogView struct {
	Enabled                    bool
	LogURL, TodayLabel         string
	HasFeed                    bool
	SinceHours, SinceMinutes   int
	SinceISO, LastLabel        string
	Running                    *BabyEntryView
	Strip                      BabyStripView
	Feeds, Wet, Dirty, SpitUps int
	FeedNote                   string
	Entries, EarlierEntries    []BabyEntryView
	Patterns                   []BabyPatternView
	PatternDays                int
	Weights                    []BabyEntryView
	WeightPoints               string
	WeightDots                 []HealthChartMarkerView
	LatestWeight               string
	Milestones                 []BabyEntryView
	Undo                       *BabyUndoView
	Error                      string
}

type BabyEntryView struct {
	ID, Kind, Title, Value, TimeLabel, TimeISO, DateLabel, Who, Note string
	Meta                                                             []string
	Running                                                          bool
}

type BabyStripView struct {
	Label            string
	Feeds, Diapers   []string
	FeedsN, DiapersN int
}

type BabyPatternView struct{ Label, Value, Unit, Note string }

// BabyUndoView offers one undo after a log or a delete.
type BabyUndoView struct {
	Message, Action, EventID string
}

// showsBabyLog keeps the log to children under three, or with no birth date.
func showsBabyLog(person people.Person, now time.Time) bool {
	if person.Relationship != "child" {
		return false
	}
	birth, ok := parseISODate(person.BirthDate)
	return !ok || birth.AddDate(3, 0, 0).After(now)
}

func clockLabel(t time.Time) string { return t.Format("15:04") }

func babyEntry(e carelog.Event, location *time.Location) BabyEntryView {
	at := e.OccurredAt.In(location)
	view := BabyEntryView{ID: e.ID, Kind: "baby", TimeLabel: clockLabel(at), TimeISO: at.Format(time.RFC3339), DateLabel: shortDate(at), Running: e.Running()}
	d := e.Details
	switch e.Type {
	case carelog.Feed:
		switch d["mode"] {
		case "bottle":
			view.Title = "Bottle feed"
		case "combo":
			view.Title = "Breast and bottle feed"
		default:
			view.Title = "Breast feed"
		}
		if side := d["side"]; side != "" {
			if side == "both" {
				view.Meta = append(view.Meta, "Both sides")
			} else {
				view.Meta = append(view.Meta, strings.ToUpper(side[:1])+side[1:])
			}
		}
		if view.Running {
			view.Meta = append(view.Meta, "Running")
		} else if m := d["duration_min"]; m != "" {
			view.Meta = append(view.Meta, m+" min")
		}
		if milk := d["milk_type"]; milk != "" {
			amount := ""
			switch {
			case d["consumed_ml"] != "" && d["offered_ml"] != "":
				amount = ", " + d["consumed_ml"] + " of " + d["offered_ml"] + " ml"
			case d["consumed_ml"] != "":
				amount = ", " + d["consumed_ml"] + " ml"
			case d["offered_ml"] != "":
				amount = ", " + d["offered_ml"] + " ml offered"
			}
			view.Meta = append(view.Meta, strings.ToUpper(milk[:1])+milk[1:]+amount)
		}
	case carelog.Diaper:
		view.Kind = "diaper"
		view.Title = map[string]string{"wet": "Wet diaper", "dirty": "Dirty diaper", "both": "Wet and dirty diaper"}[d["kind"]]
	case carelog.SpitUp:
		view.Kind = "diaper"
		view.Title = "Spit-up"
		if size := d["size"]; size != "" {
			view.Meta = append(view.Meta, strings.ToUpper(size[:1])+size[1:])
		}
	case carelog.Weight:
		view.Title = "Weight"
		view.Value = d["kg"] + " kg"
	case carelog.Milestone:
		view.Title = d["title"]
	}
	view.Who = whoLogged(e)
	view.Note = d["note"]
	return view
}

func whoLogged(e carelog.Event) string {
	switch e.Source {
	case "import":
		return "Imported"
	case "health":
		return "From Health"
	}
	local, _, _ := strings.Cut(e.ActorEmail, "@")
	return local
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func formatAmount(value float64) string {
	return strconv.FormatFloat(value, 'f', 1, 64)
}

// babyLogView builds the log from the last seven days of entries.
func babyLogView(person people.Person, events, weights []carelog.Event, now time.Time) BabyLogView {
	location := now.Location()
	view := BabyLogView{Enabled: true, LogURL: "/family/" + url.PathEscape(person.ID) + "/log", TodayLabel: now.Format("Monday 2 January")}
	today := carelog.Summarise(events, now)
	view.Feeds, view.Wet, view.Dirty, view.SpitUps = today.Feeds, today.Wet, today.Dirty, today.SpitUps
	if today.LastFeed != nil {
		view.HasFeed = true
		elapsed := now.Sub(today.LastFeed.OccurredAt)
		if elapsed < 0 {
			elapsed = 0
		}
		view.SinceHours = int(elapsed / time.Hour)
		view.SinceMinutes = int(elapsed%time.Hour) / int(time.Minute)
		view.SinceISO = today.LastFeed.OccurredAt.UTC().Format(time.RFC3339)
		last := babyEntry(*today.LastFeed, location)
		view.LastLabel = last.Title + " at " + last.TimeLabel
		for _, part := range last.Meta {
			if part != "Running" {
				view.LastLabel += ", " + strings.ToLower(part)
			}
		}
	}
	if today.Running != nil {
		running := babyEntry(*today.Running, location)
		view.Running = &running
	}
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	dayStart := now.Add(-24 * time.Hour)
	var bottleML float64
	breastFeeds := 0
	var bottleTotal, bottleCount float64
	feedsWeek, wetWeek, dirtyWeek := 0, 0, 0
	first := now
	for _, e := range events {
		if e.OccurredAt.Before(first) {
			first = e.OccurredAt
		}
		if e.Type == carelog.Feed {
			feedsWeek++
			if ml, err := strconv.ParseFloat(firstNonEmpty(e.Details["consumed_ml"], e.Details["offered_ml"]), 64); err == nil {
				bottleTotal += ml
				bottleCount++
				if !e.OccurredAt.Before(midnight) {
					bottleML += ml
				}
			}
			if e.Details["mode"] != "bottle" && !e.OccurredAt.Before(midnight) {
				breastFeeds++
			}
		}
		if e.Type == carelog.Diaper {
			if k := e.Details["kind"]; k == "wet" || k == "both" {
				wetWeek++
			}
			if k := e.Details["kind"]; k == "dirty" || k == "both" {
				dirtyWeek++
			}
		}
		if !e.OccurredAt.Before(dayStart) {
			x := strconv.FormatFloat(12+e.OccurredAt.Sub(dayStart).Hours()/24*296, 'f', 1, 64)
			switch e.Type {
			case carelog.Feed:
				view.Strip.Feeds = append(view.Strip.Feeds, x)
			case carelog.Diaper:
				view.Strip.Diapers = append(view.Strip.Diapers, x)
			}
		}
		if !e.OccurredAt.Before(midnight) && e.Type != carelog.Weight && e.Type != carelog.Milestone {
			view.Entries = append(view.Entries, babyEntry(e, location))
		}
	}
	if len(view.Entries) > 6 {
		view.Entries, view.EarlierEntries = view.Entries[:6], view.Entries[6:]
	}
	view.Strip.FeedsN, view.Strip.DiapersN = len(view.Strip.Feeds), len(view.Strip.Diapers)
	view.Strip.Label = "Last 24 hours: " + plural(view.Strip.FeedsN, "feed", "feeds") + " and " + plural(view.Strip.DiapersN, "diaper", "diapers")
	var notes []string
	if bottleML > 0 {
		notes = append(notes, strconv.FormatFloat(bottleML, 'f', -1, 64)+" ml")
	}
	if breastFeeds > 0 {
		notes = append(notes, strconv.Itoa(breastFeeds)+" breast")
	}
	view.FeedNote = strings.Join(notes, ", ")
	if len(events) > 0 {
		days := int(now.Sub(first).Hours()/24) + 1
		if days > 7 {
			days = 7
		}
		view.PatternDays = days
		per := func(n int) string { return formatAmount(float64(n) / float64(days)) }
		view.Patterns = append(view.Patterns, BabyPatternView{Label: "Feeds a day", Value: per(feedsWeek), Note: "Average of logged feeds"})
		if today.GapSamples > 0 {
			gap := today.MedianGap.Round(time.Minute)
			view.Patterns = append(view.Patterns, BabyPatternView{Label: "Gap between feeds", Value: strconv.Itoa(int(gap/time.Hour)) + "h " + strconv.Itoa(int(gap%time.Hour/time.Minute)) + "m", Note: "Median of " + plural(today.GapSamples, "gap", "gaps")})
		}
		if bottleCount > 0 {
			view.Patterns = append(view.Patterns, BabyPatternView{Label: "Bottle intake", Value: strconv.FormatFloat(bottleTotal/float64(days), 'f', 0, 64), Unit: "ml a day", Note: strconv.FormatFloat(bottleTotal/bottleCount, 'f', 0, 64) + " ml a bottle"})
		}
		view.Patterns = append(view.Patterns, BabyPatternView{Label: "Wet diapers", Value: per(wetWeek), Unit: "a day", Note: "Dirty " + per(dirtyWeek) + " a day"})
	}
	for i := len(weights) - 1; i >= 0; i-- {
		view.Weights = append(view.Weights, babyEntry(weights[i], location))
	}
	if len(weights) > 0 {
		view.LatestWeight = weights[len(weights)-1].Details["kg"]
	}
	if len(weights) > 1 {
		view.WeightPoints, view.WeightDots = weightPoints(weights)
	}
	return view
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// weightPoints plots weights against time in a 240 by 84 box.
func weightPoints(weights []carelog.Event) (string, []HealthChartMarkerView) {
	low, high := 1e9, -1e9
	for _, w := range weights {
		kg, _ := strconv.ParseFloat(w.Details["kg"], 64)
		low, high = min(low, kg), max(high, kg)
	}
	if high == low {
		high = low + 1
	}
	start, end := weights[0].OccurredAt, weights[len(weights)-1].OccurredAt
	span := end.Sub(start).Hours()
	if span <= 0 {
		span = 1
	}
	points := make([]string, 0, len(weights))
	var dots []HealthChartMarkerView
	for _, w := range weights {
		kg, _ := strconv.ParseFloat(w.Details["kg"], 64)
		x := 8 + w.OccurredAt.Sub(start).Hours()/span*224
		y := 76 - (kg-low)/(high-low)*64
		px, py := strconv.FormatFloat(x, 'f', 1, 64), strconv.FormatFloat(y, 'f', 1, 64)
		points = append(points, px+","+py)
		dots = append(dots, HealthChartMarkerView{X: px, Y: py})
	}
	return strings.Join(points, " "), dots
}

// loadBabyLog reads the log for a child's page.
func (a *App) loadBabyLog(ctx context.Context, scope policy.ActorScope, person people.Person, now time.Time, extraWeights []carelog.Event) BabyLogView {
	if a.carelog == nil || !showsBabyLog(person, now) {
		return BabyLogView{}
	}
	events, err := a.carelog.List(ctx, scope, person.ID, now.Add(-7*24*time.Hour))
	if err != nil {
		logRequestError(a.logger, ctx, "carelog_list_failed")
		return BabyLogView{Enabled: true, Error: "The baby log could not be read. Reload to try again."}
	}
	weights, _ := a.carelog.Weights(ctx, scope, person.ID)
	weights = mergeWeights(weights, extraWeights)
	view := babyLogView(person, events, weights, now)
	milestones, _ := a.carelog.Milestones(ctx, scope, person.ID)
	for _, m := range milestones {
		view.Milestones = append(view.Milestones, babyEntry(m, now.Location()))
	}
	return view
}

// entryTime reads an optional "time" (HH:MM, today or, if later than now,
// yesterday), "date" (YYYY-MM-DD) or the moment an offline phone queued the
// entry ("logged_at"). Empty means now.
func entryTime(r *http.Request, now time.Time) (time.Time, error) {
	location := now.Location()
	if clock := boundedField(r, "time", 5); clock != "" {
		parsed, err := time.ParseInLocation("15:04", clock, location)
		if err != nil {
			return time.Time{}, &carelog.InputError{Message: "Enter the time as hours and minutes."}
		}
		at := time.Date(now.Year(), now.Month(), now.Day(), parsed.Hour(), parsed.Minute(), 0, 0, location)
		if at.After(now.Add(time.Minute)) {
			at = at.AddDate(0, 0, -1)
		}
		return at, nil
	}
	if day := boundedField(r, "date", 10); day != "" {
		parsed, err := time.ParseInLocation("2006-01-02", day, location)
		if err != nil {
			return time.Time{}, &carelog.InputError{Message: "Enter a full date."}
		}
		if parsed.Year() == now.Year() && parsed.YearDay() == now.YearDay() {
			return now, nil
		}
		return parsed.Add(12 * time.Hour), nil
	}
	if queued := boundedField(r, "logged_at", 40); queued != "" {
		if at, err := time.Parse(time.RFC3339Nano, queued); err == nil && at.Before(now.Add(time.Minute)) && at.After(now.Add(-7*24*time.Hour)) {
			return at.In(location), nil
		}
	}
	return now, nil
}

// familyLog handles POST /family/{id}/log.
func (a *App) familyLog(w http.ResponseWriter, r *http.Request, scope policy.ActorScope, csrf string, person people.Person) {
	if r.Method != http.MethodPost {
		methodNotAllowedFor(w, "POST")
		return
	}
	if !a.validSessionMutation(r, a.sessionCookie(r)) {
		writeError(w, http.StatusForbidden, "request verification failed")
		return
	}
	now := a.householdNow(r.Context(), scope)
	if a.carelog == nil || !showsBabyLog(person, now) {
		http.NotFound(w, r)
		return
	}
	back := "/family/" + url.PathEscape(person.ID)
	action := boundedField(r, "action", 32)
	eventID := boundedField(r, "event", 64)
	fail := func(err error) {
		var inputErr *carelog.InputError
		message := "That entry was not saved. Reload the page and try again."
		code := http.StatusBadRequest
		if errors.As(err, &inputErr) {
			message = inputErr.Message
		} else if errors.Is(err, carelog.ErrNotFound) {
			message = "That entry is no longer in the log."
			code = http.StatusNotFound
		} else {
			logRequestError(a.logger, r.Context(), "carelog_write_failed")
			code = http.StatusInternalServerError
		}
		a.renderPerson(r.Context(), w, r, scope, csrf, person, "", message, code)
	}
	switch action {
	case "finish":
		if _, err := a.carelog.Finish(r.Context(), scope, person.ID, eventID, time.Time{}); err != nil {
			fail(err)
			return
		}
		http.Redirect(w, r, back+"?logged="+url.QueryEscape(eventID)+"#baby-log", http.StatusSeeOther)
	case "delete":
		if err := a.carelog.Delete(r.Context(), scope, person.ID, eventID); err != nil {
			fail(err)
			return
		}
		http.Redirect(w, r, back+"?removed="+url.QueryEscape(eventID)+"#baby-log", http.StatusSeeOther)
	case "restore":
		if err := a.carelog.Restore(r.Context(), scope, person.ID, eventID); err != nil {
			fail(err)
			return
		}
		http.Redirect(w, r, back+"?logged="+url.QueryEscape(eventID)+"#baby-log", http.StatusSeeOther)
	case "import_weights":
		result, err := a.importWeights(r, scope, person.ID, now.Location())
		if err != nil {
			fail(err)
			return
		}
		http.Redirect(w, r, back+"?imported="+strconv.Itoa(result.imported)+"&kept="+strconv.Itoa(result.existing)+"&skipped="+strconv.Itoa(result.skipped)+"#baby-weights", http.StatusSeeOther)
	case carelog.Feed, carelog.Diaper, carelog.SpitUp, carelog.Weight, carelog.Milestone:
		at, err := entryTime(r, now)
		if err != nil {
			fail(err)
			return
		}
		fields := map[string]string{}
		for _, key := range []string{"mode", "side", "duration_min", "milk_type", "offered_ml", "consumed_ml", "kind", "size", "kg", "title", "note"} {
			fields[key] = boundedField(r, key, 600)
		}
		event, err := a.carelog.Create(r.Context(), scope, person.ID, carelog.Input{Type: action, OccurredAt: at, ClientID: boundedField(r, "client_id", 128), Fields: fields})
		if err != nil {
			fail(err)
			return
		}
		anchor := "#baby-log"
		if action == carelog.Weight || action == carelog.Milestone {
			anchor = "#baby-weights"
		}
		http.Redirect(w, r, back+"?logged="+url.QueryEscape(event.ID)+anchor, http.StatusSeeOther)
	default:
		fail(&carelog.InputError{Message: "Choose what to log."})
	}
}

type weightImport struct{ imported, existing, skipped int }

// importWeights reads a TinyTots CSV export and adds only its weight rows.
// Each row keeps its TinyTots id, so importing the same file twice adds
// nothing new.
func (a *App) importWeights(r *http.Request, scope policy.ActorScope, personID string, location *time.Location) (weightImport, error) {
	var result weightImport
	file, _, err := r.FormFile("file")
	if err != nil {
		return result, &carelog.InputError{Message: "Choose a TinyTots CSV export to import."}
	}
	defer file.Close()
	reader := csv.NewReader(io.LimitReader(file, 1<<20))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	header, err := reader.Read()
	if err != nil {
		return result, &carelog.InputError{Message: "The file could not be read as a CSV."}
	}
	columns := map[string]int{}
	for index, name := range header {
		columns[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "\uFEFF")))] = index
	}
	for _, need := range []string{"id", "type", "occurred_at", "kg"} {
		if _, ok := columns[need]; !ok {
			return result, &carelog.InputError{Message: "This is not a TinyTots export. The header needs id, type, occurred_at and kg."}
		}
	}
	cell := func(row []string, name string) string {
		if i := columns[name]; i < len(row) {
			return strings.TrimSpace(row[i])
		}
		return ""
	}
	type pending struct {
		id, kg string
		at     time.Time
	}
	var rows []pending
	for line := 2; ; line++ {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return result, &carelog.InputError{Message: "Line " + strconv.Itoa(line) + " could not be read. Nothing was imported."}
		}
		if cell(row, "type") != carelog.Weight {
			result.skipped++
			continue
		}
		raw := cell(row, "occurred_at")
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			at, err = time.ParseInLocation("2006-01-02T15:04", raw, location)
		}
		id := cell(row, "id")
		if err != nil || id == "" {
			return result, &carelog.InputError{Message: "Line " + strconv.Itoa(line) + " needs an id and a time. Nothing was imported."}
		}
		if _, err := carelog.Validate(carelog.Input{Type: carelog.Weight, Fields: map[string]string{"kg": cell(row, "kg")}}); err != nil {
			return result, &carelog.InputError{Message: "Line " + strconv.Itoa(line) + ": " + err.Error() + " Nothing was imported."}
		}
		rows = append(rows, pending{id: id, kg: cell(row, "kg"), at: at})
		if len(rows) > 2000 {
			return result, &carelog.InputError{Message: "The file has more than 2,000 weights. Split it into smaller files."}
		}
	}
	for _, row := range rows {
		clientID := "import:" + row.id
		event, err := a.carelog.Create(r.Context(), scope, personID, carelog.Input{Type: carelog.Weight, OccurredAt: row.at, ClientID: clientID, Source: "import", Fields: map[string]string{"kg": row.kg}})
		if err != nil {
			return result, err
		}
		if event.Duplicate {
			result.existing++
		} else {
			result.imported++
		}
	}
	return result, nil
}

// babyUndo turns the redirect after a log, a delete or an import into one
// status line, with an undo for the first two.
func (a *App) babyUndo(ctx context.Context, scope policy.ActorScope, person people.Person, query url.Values, status string) (*BabyUndoView, string) {
	if id := query.Get("logged"); id != "" && len(id) <= 64 {
		event, err := a.carelog.Get(ctx, scope, person.ID, id)
		if err != nil {
			return nil, status
		}
		entry := babyEntry(event, a.householdNow(ctx, scope).Location())
		message := entry.Title + " logged at " + entry.TimeLabel + "."
		if entry.Running {
			message = entry.Title + " started at " + entry.TimeLabel + "."
		}
		return &BabyUndoView{Message: message, Action: "delete", EventID: event.ID}, ""
	}
	if id := query.Get("removed"); id != "" && len(id) <= 64 {
		return &BabyUndoView{Message: "Entry removed from the log.", Action: "restore", EventID: id}, ""
	}
	if raw := query.Get("imported"); raw != "" {
		imported, _ := strconv.Atoi(raw)
		kept, _ := strconv.Atoi(query.Get("kept"))
		skipped, _ := strconv.Atoi(query.Get("skipped"))
		line := "Imported " + plural(imported, "weight", "weights") + "."
		if kept > 0 {
			line += " " + pluralWas(kept) + " already in the log."
		}
		if skipped > 0 {
			line += " Skipped " + plural(skipped, "row that is not a weight", "rows that are not weights") + "."
		}
		return nil, line
	}
	return nil, status
}

func pluralWas(n int) string {
	if n == 1 {
		return "1 weight was"
	}
	return strconv.Itoa(n) + " weights were"
}

// BabyPanelView is the baby panel on Home: the clock since the last feed and
// today's counts, with a way into the log.
type BabyPanelView struct {
	Name, URL, SinceISO, LastLabel, TodayLine string
	HasFeed, Running                          bool
	SinceHours, SinceMinutes                  int
	Strip                                     BabyStripView
}

func (a *App) babyPanels(ctx context.Context, scope policy.ActorScope) []BabyPanelView {
	if a.carelog == nil {
		return nil
	}
	list, err := a.people.List(ctx, scope)
	if err != nil {
		return nil
	}
	now := a.householdNow(ctx, scope)
	var panels []BabyPanelView
	for _, person := range list {
		if !showsBabyLog(person, now) {
			continue
		}
		events, err := a.carelog.List(ctx, scope, person.ID, now.Add(-7*24*time.Hour))
		if err != nil {
			continue
		}
		log := babyLogView(person, events, nil, now)
		diapers := 0
		for _, e := range events {
			if e.Type == carelog.Diaper && !e.OccurredAt.Before(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())) {
				diapers++
			}
		}
		panels = append(panels, BabyPanelView{Name: person.Name, URL: "/family/" + url.PathEscape(person.ID) + "#baby-log", SinceISO: log.SinceISO, LastLabel: log.LastLabel, HasFeed: log.HasFeed, Running: log.Running != nil, SinceHours: log.SinceHours, SinceMinutes: log.SinceMinutes, Strip: log.Strip, TodayLine: plural(log.Feeds, "feed", "feeds") + " and " + plural(diapers, "diaper", "diapers") + " today"})
	}
	return panels
}

// healthWeights turns Health weight readings in kg for one person into log
// entries, so the child's page shows one weight list.
func healthWeights(observations []health.Observation, name string) []carelog.Event {
	var out []carelog.Event
	for _, o := range observations {
		if !strings.EqualFold(strings.TrimSpace(o.Subject), name) || health.CanonicalAnalyte(o.Analyte) != "Weight" || !strings.EqualFold(strings.TrimSpace(o.Unit), "kg") {
			continue
		}
		day, ok := parseISODate(o.ObservedOn)
		if !ok {
			continue
		}
		out = append(out, carelog.Event{ID: "health-" + o.ID, Type: carelog.Weight, Source: "health", OccurredAt: day.Add(12 * time.Hour), Details: map[string]string{"kg": o.Value.PlainString()}})
	}
	return out
}

// mergeWeights adds Health readings that are not already logged on the same
// day with the same value, keeping the list oldest first.
func mergeWeights(logged, extra []carelog.Event) []carelog.Event {
	seen := map[string]bool{}
	key := func(e carelog.Event) string {
		kg, _ := strconv.ParseFloat(e.Details["kg"], 64)
		return e.OccurredAt.UTC().Format("2006-01-02") + "|" + strconv.FormatFloat(kg, 'f', 3, 64)
	}
	for _, e := range logged {
		seen[key(e)] = true
	}
	out := append([]carelog.Event(nil), logged...)
	for _, e := range extra {
		if !seen[key(e)] {
			seen[key(e)] = true
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].OccurredAt.Before(out[j].OccurredAt) })
	return out
}

func withoutWeight(series []HealthSeriesView) []HealthSeriesView {
	out := series[:0:0]
	for _, s := range series {
		if health.CanonicalAnalyte(s.Analyte) != "Weight" {
			out = append(out, s)
		}
	}
	return out
}
