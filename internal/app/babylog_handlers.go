package app

import (
	"context"
	"errors"
	"math"
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
	LatestWeight               string
	Milestones                 []BabyEntryView
	Undo                       *BabyUndoView
	Error                      string
	// ErrorCard names the log pad card a refused entry came from, so the
	// message and the typed values return to that card.
	ErrorCard string
	Values    map[string]string
	// NextSide preselects the breast not used last; LastSide states it.
	NextSide, LastSide string
	// RunningX is where a running feed starts on the strip, if it does.
	RunningX string
	// Focus is the measure the hero leads with; see childFocus.
	Focus                         string
	PatternsURL, SettingsURL      string
	WeightLabel, WeightChangeNote string
	WeightData                    string
	BottleToday, BottleNote       string
	DiapersToday                  int
	DiaperNote                    string
}

type BabyEntryView struct {
	ID, Kind, Title, Value, TimeLabel, TimeISO, DateLabel, Who, Note string
	Type, Clock, Relative                                            string
	Meta                                                             []string
	Details                                                          map[string]string
	Running                                                          bool
}

type BabyStripView struct {
	Label            string
	Feeds, Diapers   []StripMark
	FeedsN, DiapersN int
}

// StripMark is one entry on the 24-hour strip, with what its tooltip says.
type StripMark struct{ X, Left, Title, Line string }

type BabyPatternView struct{ Key, Label, Value, Unit, Note string }

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
	view := BabyEntryView{ID: e.ID, Kind: "baby", TimeLabel: clockLabel(at), TimeISO: at.Format(time.RFC3339), DateLabel: shortDate(at), Running: e.Running(), Type: e.Type, Clock: at.Format("15:04"), Details: e.Details}
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
	var lastSideAt time.Time
	base := "/family/" + url.PathEscape(person.ID)
	view := BabyLogView{Enabled: true, LogURL: base + "/log", PatternsURL: base + "/patterns", SettingsURL: base + "/settings", TodayLabel: now.Format("Monday 2 January"), Focus: childFocus(person)}
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
		start := today.Running.OccurredAt
		if start.Before(now.Add(-24 * time.Hour)) {
			start = now.Add(-24 * time.Hour)
		}
		view.RunningX = strconv.FormatFloat(12+start.Sub(now.Add(-24*time.Hour)).Hours()/24*296, 'f', 1, 64)
	}
	view.NextSide = "left"
	for _, e := range events {
		if e.Type == carelog.Feed && e.Details["side"] != "" {
			if view.LastSide == "" || e.OccurredAt.After(lastSideAt) {
				view.LastSide, lastSideAt = e.Details["side"], e.OccurredAt
			}
		}
	}
	switch view.LastSide {
	case "left":
		view.NextSide = "right"
	case "right":
		view.NextSide = "left"
	case "both":
		view.NextSide = "both"
	}
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	dayStart := now.Add(-24 * time.Hour)
	var bottleML float64
	bottlesToday := 0
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
					bottlesToday++
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
			at := 12 + e.OccurredAt.Sub(dayStart).Hours()/24*296
			entry := babyEntry(e, location)
			line := entry.TimeLabel + ", " + agoLabel(now.Sub(e.OccurredAt))
			if len(entry.Meta) > 0 {
				line = strings.Join(entry.Meta, ", ") + ". " + line
			}
			mark := StripMark{X: strconv.FormatFloat(at, 'f', 1, 64), Left: strconv.FormatFloat(at-6, 'f', 1, 64), Title: entry.Title, Line: line}
			switch e.Type {
			case carelog.Feed:
				view.Strip.Feeds = append(view.Strip.Feeds, mark)
			case carelog.Diaper:
				view.Strip.Diapers = append(view.Strip.Diapers, mark)
			}
		}
		if !e.OccurredAt.Before(midnight) && e.Type != carelog.Weight && e.Type != carelog.Milestone {
			entry := babyEntry(e, location)
			entry.Relative = agoLabel(now.Sub(e.OccurredAt))
			view.Entries = append(view.Entries, entry)
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
	view.BottleToday = strconv.FormatFloat(bottleML, 'f', -1, 64)
	view.BottleNote = plural(bottlesToday, "bottle", "bottles") + " since midnight"
	view.DiapersToday = today.Wet + today.Dirty
	for _, e := range events {
		if e.Type == carelog.Diaper && e.Details["kind"] == "both" && !e.OccurredAt.Before(midnight) {
			view.DiapersToday--
		}
	}
	view.DiaperNote = strconv.Itoa(view.Wet) + " wet and " + strconv.Itoa(view.Dirty) + " dirty since midnight"
	if len(events) > 0 {
		days := int(now.Sub(first).Hours()/24) + 1
		if days > 7 {
			days = 7
		}
		view.PatternDays = days
		per := func(n int) string { return formatAmount(float64(n) / float64(days)) }
		view.Patterns = append(view.Patterns, BabyPatternView{Key: focusFeeds, Label: "Feeds a day", Value: per(feedsWeek), Note: "Average of logged feeds"})
		if today.GapSamples > 0 {
			gap := today.MedianGap.Round(time.Minute)
			view.Patterns = append(view.Patterns, BabyPatternView{Label: "Gap between feeds", Value: strconv.Itoa(int(gap/time.Hour)) + "h " + strconv.Itoa(int(gap%time.Hour/time.Minute)) + "m", Note: "Median of " + plural(today.GapSamples, "gap", "gaps")})
		}
		if bottleCount > 0 {
			view.Patterns = append(view.Patterns, BabyPatternView{Key: focusBottle, Label: "Bottle intake", Value: strconv.FormatFloat(bottleTotal/float64(days), 'f', 0, 64), Unit: "ml a day", Note: strconv.FormatFloat(bottleTotal/bottleCount, 'f', 0, 64) + " ml a bottle"})
		}
		view.Patterns = append(view.Patterns, BabyPatternView{Key: focusDiapers, Label: "Wet diapers", Value: per(wetWeek), Unit: "a day", Note: "Dirty " + per(dirtyWeek) + " a day"})
		// The log keeps two figures, the chosen measure's first; Patterns has the rest.
		sort.SliceStable(view.Patterns, func(i, j int) bool { return view.Patterns[i].Key == view.Focus && view.Patterns[j].Key != view.Focus })
		if len(view.Patterns) > 2 {
			view.Patterns = view.Patterns[:2]
		}
		over := "over the last " + plural(days, "day", "days")
		if days == 1 {
			over = "since logging began today"
		}
		if bottleCount > 0 {
			view.BottleNote += ". " + strconv.FormatFloat(bottleTotal/float64(days), 'f', 0, 64) + " ml a day " + over
		}
		view.DiaperNote += ". " + per(wetWeek) + " wet a day " + over
	}
	for i := len(weights) - 1; i >= 0; i-- {
		view.Weights = append(view.Weights, babyEntry(weights[i], location))
	}
	if len(weights) > 0 {
		last := weights[len(weights)-1]
		view.LatestWeight = last.Details["kg"]
		view.WeightLabel = "Logged " + shortDate(last.OccurredAt.In(location))
		if len(weights) > 1 {
			before := weights[len(weights)-2]
			latestKG, priorKG := parseKG(last), parseKG(before)
			daysBetween := int(math.Round(last.OccurredAt.Sub(before.OccurredAt).Hours() / 24))
			view.WeightChangeNote = weightChange(latestKG-priorKG) + " since " + shortDate(before.OccurredAt.In(location))
			if daysBetween >= 1 {
				view.WeightChangeNote += ", " + plural(daysBetween, "day", "days") + " earlier"
			}
		}
		start := weights[0].OccurredAt.In(location)
		start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, location)
		view.WeightData = weightChartData(weights, start, now).JSON()
	}
	return view
}

func parseKG(e carelog.Event) float64 {
	kg, _ := strconv.ParseFloat(e.Details["kg"], 64)
	return kg
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
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
		a.notFound(w, r, "There’s no baby log here", "The baby log is kept for a child under three.", "/family/"+url.PathEscape(person.ID), "Back to "+person.Name)
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
		http.Redirect(w, r, back+"?logged="+url.QueryEscape(eventID), http.StatusSeeOther)
	case "edit":
		at, err := entryTime(r, now)
		if err != nil {
			fail(err)
			return
		}
		fields := map[string]string{}
		for _, key := range []string{"mode", "side", "duration_min", "milk_type", "offered_ml", "consumed_ml", "kind", "size", "kg", "title"} {
			if value := boundedField(r, key, 600); value != "" {
				fields[key] = value
			}
		}
		if _, err := a.carelog.Update(r.Context(), scope, person.ID, eventID, at, fields); err != nil {
			fail(err)
			return
		}
		http.Redirect(w, r, back+"?changed="+url.QueryEscape(eventID), http.StatusSeeOther)
	case "delete":
		if err := a.carelog.Delete(r.Context(), scope, person.ID, eventID); err != nil {
			fail(err)
			return
		}
		http.Redirect(w, r, back+"?removed="+url.QueryEscape(eventID), http.StatusSeeOther)
	case "restore":
		if err := a.carelog.Restore(r.Context(), scope, person.ID, eventID); err != nil {
			fail(err)
			return
		}
		http.Redirect(w, r, back+"?logged="+url.QueryEscape(eventID), http.StatusSeeOther)
	case "import", "import_weights":
		result, err := a.importEntries(r, scope, person, now.Location())
		if err != nil {
			var inputErr *carelog.InputError
			message := "The file was not imported. Reload the page and try again."
			if errors.As(err, &inputErr) {
				message = inputErr.Message
			} else {
				logRequestError(a.logger, r.Context(), "carelog_import_failed")
			}
			a.renderChildSettings(r.Context(), w, scope, csrf, person, "", "", http.StatusBadRequest, "", message, "")
			return
		}
		http.Redirect(w, r, back+"/settings?imported="+strconv.Itoa(result.imported)+"&kept="+strconv.Itoa(result.existing)+"&skipped="+strconv.Itoa(result.skipped)+"&batch="+result.batch+"#import", http.StatusSeeOther)
	case "undo_import":
		removed, err := a.carelog.DeleteBatch(r.Context(), scope, person.ID, boundedField(r, "batch", 32))
		if err != nil {
			a.renderChildSettings(r.Context(), w, scope, csrf, person, "", "", http.StatusBadRequest, "", "That import could not be undone. Reload the page and try again.", "")
			return
		}
		http.Redirect(w, r, back+"/settings?undone="+strconv.Itoa(removed)+"#import", http.StatusSeeOther)
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
		// No fragment: the page script moves focus to the status message, and a
		// fragment would pull focus back to the page.
		http.Redirect(w, r, back+"?logged="+url.QueryEscape(event.ID), http.StatusSeeOther)
	default:
		fail(&carelog.InputError{Message: "Choose what to log."})
	}
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
		if event.Type == carelog.Diaper {
			if recent, err := a.carelog.List(ctx, scope, person.ID, event.OccurredAt.Add(-2*time.Minute)); err == nil {
				for _, other := range recent {
					if other.ID != event.ID && other.Type == carelog.Diaper && other.Details["kind"] == event.Details["kind"] && !other.OccurredAt.After(event.OccurredAt) {
						gap := "under a minute"
						if minutes := int(event.OccurredAt.Sub(other.OccurredAt).Minutes()); minutes >= 1 {
							gap = strconv.Itoa(minutes) + " min"
						}
						message += " Another " + strings.ToLower(entry.Title) + " was logged " + gap + " before it."
						break
					}
				}
			}
		}
		return &BabyUndoView{Message: message, Action: "delete", EventID: event.ID}, ""
	}
	if id := query.Get("changed"); id != "" && len(id) <= 64 {
		event, err := a.carelog.Get(ctx, scope, person.ID, id)
		if err != nil {
			return nil, status
		}
		entry := babyEntry(event, a.householdNow(ctx, scope).Location())
		return nil, entry.Title + " at " + entry.TimeLabel + " updated."
	}
	if id := query.Get("removed"); id != "" && len(id) <= 64 {
		return &BabyUndoView{Message: "Entry removed from the log.", Action: "restore", EventID: id}, ""
	}
	return nil, status
}

// BabyPanelView is the baby panel on Home: the clock since the last feed and
// today's counts, with a way into the log.
type BabyPanelView struct {
	Name, URL, SinceISO, LastLabel, TodayLine string
	HasFeed, Running                          bool
	SinceHours, SinceMinutes                  int
	Strip                                     BabyStripView
	// Focus, with Label, Value, Unit and Note, is what the panel leads
	// with when the child's settings choose something other than feeds.
	Focus, Label, Value, Unit, Note string
	ActionURL, ActionLabel          string
	WeightData, RunningX            string
	HasChart                        bool
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
		var weights []carelog.Event
		if childFocus(person) == focusWeight {
			weights, _ = a.carelog.Weights(ctx, scope, person.ID)
		}
		log := babyLogView(person, events, weights, now)
		diapers := 0
		for _, e := range events {
			if e.Type == carelog.Diaper && !e.OccurredAt.Before(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())) {
				diapers++
			}
		}
		panel := BabyPanelView{Name: person.Name, URL: "/family/" + url.PathEscape(person.ID) + "#baby-log", SinceISO: log.SinceISO, LastLabel: log.LastLabel, HasFeed: log.HasFeed, Running: log.Running != nil, SinceHours: log.SinceHours, SinceMinutes: log.SinceMinutes, Strip: log.Strip, RunningX: log.RunningX, TodayLine: plural(log.Feeds, "feed", "feeds") + " and " + plural(diapers, "diaper", "diapers") + " today", Focus: log.Focus}
		page := "/family/" + url.PathEscape(person.ID)
		switch log.Focus {
		case focusWeight:
			panel.Label, panel.Unit, panel.ActionURL, panel.ActionLabel = "Latest weight", "kg", page+"#baby-log", "Log a weight"
			panel.Value, panel.Note = log.LatestWeight, firstNonEmpty(log.WeightChangeNote, log.WeightLabel)
			if log.LatestWeight == "" {
				panel.Note = "No weights logged yet."
			}
			panel.WeightData, panel.HasChart = log.WeightData, len(weights) > 1
		case focusBottle:
			panel.Label, panel.Value, panel.Unit, panel.Note, panel.ActionURL, panel.ActionLabel = "Bottle intake today", log.BottleToday, "ml", log.BottleNote, page+"#baby-log", "Log a bottle"
		case focusDiapers:
			panel.Label, panel.Value, panel.Note, panel.ActionURL, panel.ActionLabel = "Diapers today", strconv.Itoa(log.DiapersToday), log.DiaperNote, page+"#baby-log", "Log a diaper"
		}
		panels = append(panels, panel)
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

// noteBabyWeights tells the Health page when a child's weights live in the
// baby log, so two counts of the same thing never disagree silently.
func (a *App) noteBabyWeights(ctx context.Context, scope policy.ActorScope, list []people.Person, now time.Time, view *HealthView) {
	if a.carelog == nil {
		return
	}
	notes := map[string]string{}
	for _, person := range list {
		if !showsBabyLog(person, now) {
			continue
		}
		logged, err := a.carelog.Weights(ctx, scope, person.ID)
		if err != nil || len(logged) == 0 {
			continue
		}
		notes[strings.ToLower(person.Name)] = "Health records only. " + person.Name + "'s baby log has " + plural(len(logged), "weight", "weights") + "."
	}
	if len(notes) == 0 {
		return
	}
	apply := func(series []HealthSeriesView) {
		for i := range series {
			if health.CanonicalAnalyte(series[i].Analyte) == "Weight" {
				subject := series[i].Subject
				if subject == "" {
					subject = view.Person
				}
				series[i].Note = notes[strings.ToLower(strings.TrimSpace(subject))]
			}
		}
	}
	apply(view.Series)
	for g := range view.Groups {
		apply(view.Groups[g].Series)
	}
}

func babyErrorCard(r *http.Request) (string, map[string]string) {
	values := map[string]string{}
	for _, key := range []string{"side", "duration_min", "milk_type", "offered_ml", "consumed_ml", "kg", "title", "date", "time"} {
		values[key] = boundedField(r, key, 200)
	}
	switch action := boundedField(r, "action", 32); action {
	case carelog.Feed:
		if boundedField(r, "mode", 16) == "bottle" {
			return "bottle", values
		}
		return "breast", values
	case carelog.Diaper, carelog.SpitUp, carelog.Weight, carelog.Milestone:
		return action, values
	}
	return "", values
}

// QuickBaby feeds the baby row at the top of the Add sheet.
type QuickBaby struct {
	Name     string `json:"name"`
	LogURL   string `json:"logUrl"`
	PageURL  string `json:"pageUrl"`
	NextSide string `json:"nextSide"`
	Running  string `json:"running,omitempty"`
}

// familyQuick lists the young children whose log the Add sheet can reach.
func (a *App) familyQuick(w http.ResponseWriter, r *http.Request) {
	if !allowsRead(r.Method) {
		methodNotAllowed(w)
		return
	}
	scope, ok := a.sessionScope(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "sign in again")
		return
	}
	out := []QuickBaby{}
	if a.carelog != nil {
		if list, err := a.people.List(r.Context(), scope); err == nil {
			now := a.householdNow(r.Context(), scope)
			for _, person := range list {
				if !showsBabyLog(person, now) {
					continue
				}
				events, err := a.carelog.List(r.Context(), scope, person.ID, now.Add(-7*24*time.Hour))
				if err != nil {
					continue
				}
				log := babyLogView(person, events, nil, now)
				item := QuickBaby{Name: person.Name, LogURL: log.LogURL, PageURL: "/family/" + url.PathEscape(person.ID) + "#baby-log", NextSide: log.NextSide}
				if log.Running != nil {
					item.Running = log.Running.ID
				}
				out = append(out, item)
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// agoLabel states how long ago something happened, in the plain words the
// timeline uses.
func agoLabel(d time.Duration) string {
	switch minutes := int(d.Minutes()); {
	case minutes < 1:
		return "just now"
	case minutes < 60:
		return strconv.Itoa(minutes) + " min ago"
	default:
		return strconv.Itoa(minutes/60) + " h ago"
	}
}
