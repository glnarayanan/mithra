package app

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/glnarayanan/mithra/internal/people"
	"github.com/glnarayanan/mithra/internal/policy"
)

// ChildSettingsView is a child's settings page: what their page leads with,
// importing and downloading the log, and their details.
type ChildSettingsView struct {
	Navigation    []NavigationItem
	CSRF          string
	Status, Error string
	Person        FamilyPersonView
	Relationships []FamilyOptionView
	Focus         []FocusOption
	HasLog        bool
	LogURL        string
	SettingsURL   string
	SampleURL     string
	ExportURL     string
	Columns       []LogColumnHelp
	Entries       int
}

// familyChildPage serves the pages under /family/{id}/ besides the log.
func (a *App) familyChildPage(w http.ResponseWriter, r *http.Request, scope policy.ActorScope, csrf string, person people.Person, sub string) {
	now := a.householdNow(r.Context(), scope)
	if a.carelog == nil || !showsBabyLog(person, now) {
		a.notFound(w, r, "There’s no baby log here", "The baby log is kept for a child under three.", "/family/"+url.PathEscape(person.ID), "Back to "+person.Name)
		return
	}
	switch sub {
	case "patterns":
		if !allowsRead(r.Method) {
			methodNotAllowed(w)
			return
		}
		a.renderPatterns(w, r, scope, csrf, person, now)
	case "sample.csv":
		if !allowsRead(r.Method) {
			methodNotAllowed(w)
			return
		}
		writeCSV(w, "baby-log-sample.csv", []byte(sampleLogCSV))
	case "export.csv":
		if !allowsRead(r.Method) {
			methodNotAllowed(w)
			return
		}
		events, err := a.carelog.List(r.Context(), scope, person.ID, time.Time{})
		if err != nil {
			logRequestError(a.logger, r.Context(), "carelog_export_failed")
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		writeCSV(w, "baby-log-"+now.Format("2006-01-02")+".csv", logCSV(events, now.Location()))
	case "settings":
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			status := ""
			query := r.URL.Query()
			if raw := query.Get("imported"); raw != "" {
				imported, _ := strconv.Atoi(raw)
				kept, _ := strconv.Atoi(query.Get("kept"))
				skipped, _ := strconv.Atoi(query.Get("skipped"))
				status = importStatus(imported, kept, skipped)
			}
			switch query.Get("saved") {
			case "focus":
				status = "Saved. " + person.Name + "’s page now leads with " + lowerFirst(focusLabels[childFocus(person)][0]) + "."
			case "details":
				status = "Details saved."
			}
			a.renderChildSettings(r.Context(), w, scope, csrf, person, status, "", http.StatusOK)
		case http.MethodPost:
			if !a.validSessionMutation(r, a.sessionCookie(r)) {
				writeError(w, http.StatusForbidden, "request verification failed")
				return
			}
			if err := a.people.SetFocus(r.Context(), scope, person.ID, boundedField(r, "focus", 16)); err != nil {
				a.renderChildSettings(r.Context(), w, scope, csrf, person, "", "Choose one of the measures.", http.StatusBadRequest)
				return
			}
			http.Redirect(w, r, "/family/"+url.PathEscape(person.ID)+"/settings?saved=focus", http.StatusSeeOther)
		default:
			methodNotAllowedFor(w, "GET, HEAD, POST")
		}
	}
}

func writeCSV(w http.ResponseWriter, name string, body []byte) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Write(body)
}

func (a *App) renderChildSettings(ctx context.Context, w http.ResponseWriter, scope policy.ActorScope, csrf string, person people.Person, status, problem string, code int) {
	now := a.householdNow(ctx, scope)
	base := "/family/" + url.PathEscape(person.ID)
	view := ChildSettingsView{Navigation: navigationForPath("/family/"), CSRF: csrf, Status: status, Error: problem, Person: familyPersonView(person, now, 0), Relationships: relationshipOptions(person.Relationship), Focus: focusOptions(childFocus(person)), HasLog: true, LogURL: base + "/log", SettingsURL: base + "/settings", SampleURL: base + "/sample.csv", ExportURL: base + "/export.csv", Columns: logColumnHelp}
	if events, err := a.carelog.List(ctx, scope, person.ID, time.Time{}); err == nil {
		view.Entries = len(events)
	}
	a.renderPage(ctx, w, "child-settings.html", view, code, "child_settings_render_failed")
}

func (a *App) renderPatterns(w http.ResponseWriter, r *http.Request, scope policy.ActorScope, csrf string, person people.Person, now time.Time) {
	days := patternDays(r.URL.Query().Get("days"))
	events, err := a.carelog.List(r.Context(), scope, person.ID, dayKeys(now, days)[0])
	if err != nil {
		logRequestError(a.logger, r.Context(), "carelog_list_failed")
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	weights, _ := a.carelog.Weights(r.Context(), scope, person.ID)
	view := patternsView(person, events, weights, now, days)
	view.Navigation, view.CSRF, view.Person = navigationForPath("/family/"), csrf, familyPersonView(person, now, 0)
	a.renderPage(r.Context(), w, "patterns.html", view, http.StatusOK, "patterns_render_failed")
}

func (a *App) renderPage(ctx context.Context, w http.ResponseWriter, name string, view any, code int, failure string) {
	rendered := newBufferedResponse(maxResponseBodyBytes)
	if err := a.templates.ExecuteTemplate(rendered, name, view); err != nil || rendered.overflow {
		logRequestError(a.logger, ctx, failure)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if code != http.StatusOK {
		w.WriteHeader(code)
	}
	a.localize(ctx, rendered)
	rendered.commit(w)
}
