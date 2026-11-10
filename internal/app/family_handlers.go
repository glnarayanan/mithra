package app

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/glnarayanan/mithra/internal/health"
	"github.com/glnarayanan/mithra/internal/people"
	"github.com/glnarayanan/mithra/internal/policy"
)

// FamilyView is the Family page: the two adults plus the people they care for.
type FamilyView struct {
	Navigation    []NavigationItem
	CSRF          string
	Status        string
	Error         string
	Adults        []FamilyAdultView
	People        []FamilyPersonView
	Relationships []FamilyOptionView
}

type FamilyAdultView struct{ Label, Role string }

type FamilyOptionView struct {
	Value, Label string
	Selected     bool
}

type FamilyPersonView struct {
	ID, Name, Relationship, RelationshipLabel, BirthDate, BirthLabel, Age, URL, HealthURL, Fact string
	Version                                                                                     int64
	Private                                                                                     bool
	HealthRecords                                                                               int
}

// PersonView is one person's page.
type PersonView struct {
	Navigation    []NavigationItem
	CSRF          string
	Status        string
	Person        FamilyPersonView
	Relationships []FamilyOptionView
	Health        HealthView
	Care          CareView
	Catalog       []HealthCatalogOption
	Baby          BabyLogView
}

var relationshipLabels = map[string]string{"child": "Child", "parent": "Parent", "adult": "Adult", "other": "Other"}

func relationshipOptions(selected string) []FamilyOptionView {
	options := make([]FamilyOptionView, 0, len(people.Relationships))
	for _, value := range people.Relationships {
		options = append(options, FamilyOptionView{Value: value, Label: relationshipLabels[value], Selected: value == selected})
	}
	return options
}

func familyPersonView(person people.Person, now time.Time, records int) FamilyPersonView {
	view := FamilyPersonView{ID: person.ID, Name: person.Name, Relationship: person.Relationship, RelationshipLabel: relationshipLabels[person.Relationship], BirthDate: person.BirthDate, Age: people.Age(person.BirthDate, now), URL: "/family/" + url.PathEscape(person.ID), HealthURL: healthURL("all", "", person.Name), Version: person.Version, Private: person.Visibility == policy.Personal, HealthRecords: records}
	if date, ok := parseISODate(person.BirthDate); ok {
		view.BirthLabel = "Born " + date.Format("2 Jan 2006")
	}
	return view
}

func (a *App) family(w http.ResponseWriter, r *http.Request) {
	scope, csrf, ok := a.authenticated(r)
	if !ok {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		status := ""
		switch r.URL.Query().Get("done") {
		case "added":
			status = "Person added."
		case "archived":
			status = "Person removed from Family. Records that name them are unchanged."
		}
		a.renderFamily(r.Context(), w, scope, csrf, status, "", http.StatusOK)
	case http.MethodPost:
		if !a.validSessionMutation(r, a.sessionCookie(r)) {
			writeError(w, http.StatusForbidden, "request verification failed")
			return
		}
		draft := familyDraft(r)
		if _, err := a.people.Create(r.Context(), scope, draft); err != nil {
			a.renderFamily(r.Context(), w, scope, csrf, "", "Add a name of up to 80 characters, choose who they are and use a full date of birth, or leave it blank.", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/family?done=added", http.StatusSeeOther)
	default:
		methodNotAllowedFor(w, "GET, HEAD, POST")
	}
}

func familyDraft(r *http.Request) people.Draft {
	visibility := policy.Shared
	if r.PostForm.Get("visibility") == string(policy.Personal) {
		visibility = policy.Personal
	}
	return people.Draft{Name: boundedField(r, "name", 200), Relationship: boundedField(r, "relationship", 16), BirthDate: boundedField(r, "birth_date", 10), Visibility: visibility}
}

func (a *App) renderFamily(ctx context.Context, w http.ResponseWriter, scope policy.ActorScope, csrf, status, problem string, code int) {
	view := FamilyView{Navigation: navigationForPath("/family"), CSRF: csrf, Status: status, Error: problem, Relationships: relationshipOptions("child")}
	members, err := a.auth.Members(ctx, scope)
	if err != nil {
		logRequestError(a.logger, ctx, "family_members_failed")
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	for _, member := range members {
		if member.Status != "active" {
			continue
		}
		role := "Partner"
		if member.Role == "owner" {
			role = "Owner"
		}
		view.Adults = append(view.Adults, FamilyAdultView{Label: member.Email, Role: role})
	}
	list, err := a.people.List(ctx, scope)
	if err != nil {
		logRequestError(a.logger, ctx, "family_people_failed")
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	counts := map[string]int{}
	if summary, err := a.healthRecords.Summarize(ctx, scope, health.AllRecords); err == nil {
		for _, observation := range summary.Observations {
			counts[strings.TrimSpace(observation.Subject)]++
		}
	}
	now := a.householdNow(ctx, scope)
	facts := map[string]string{}
	for _, panel := range a.babyPanels(ctx, scope) {
		switch {
		case panel.Running:
			facts[strings.TrimSuffix(panel.URL, "#baby-log")] = "Feeding now"
		case panel.HasFeed:
			facts[strings.TrimSuffix(panel.URL, "#baby-log")] = "Fed " + strconv.Itoa(panel.SinceHours) + "h " + strconv.Itoa(panel.SinceMinutes) + "m ago"
		}
	}
	for _, person := range list {
		row := familyPersonView(person, now, counts[person.Name])
		row.Fact = facts[row.URL]
		view.People = append(view.People, row)
	}
	rendered := newBufferedResponse(maxResponseBodyBytes)
	if err := a.templates.ExecuteTemplate(rendered, "family.html", view); err != nil || rendered.overflow {
		logRequestError(a.logger, ctx, "family_render_failed")
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if code != http.StatusOK {
		w.WriteHeader(code)
	}
	rendered.commit(w)
}

func (a *App) familyPerson(w http.ResponseWriter, r *http.Request) {
	scope, csrf, ok := a.authenticated(r)
	if !ok {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	id, sub, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/family/"), "/")
	if id == "" || len(id) > 64 || (sub != "" && sub != "log") {
		a.notFound(w, r, "That person isn’t in Family", "They may have been removed, or the address is mistyped.", "/family", "Back to Family")
		return
	}
	person, err := a.people.Get(r.Context(), scope, id)
	if err != nil {
		a.notFound(w, r, "That person isn’t in Family", "They may have been removed, or the address is mistyped.", "/family", "Back to Family")
		return
	}
	if sub == "log" {
		a.familyLog(w, r, scope, csrf, person)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodPost:
		if !a.validSessionMutation(r, a.sessionCookie(r)) {
			writeError(w, http.StatusForbidden, "request verification failed")
			return
		}
		version, err := strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
		if err != nil || version < 1 {
			writeError(w, http.StatusBadRequest, "version is required")
			return
		}
		if r.PostForm.Get("action") == "archive" {
			if err := a.people.Archive(r.Context(), scope, id, version); err != nil {
				writeError(w, http.StatusConflict, "this person changed; reload and try again")
				return
			}
			http.Redirect(w, r, "/family?done=archived", http.StatusSeeOther)
			return
		}
		if _, err := a.people.Update(r.Context(), scope, id, version, familyDraft(r)); err != nil {
			writeError(w, http.StatusConflict, "check the details, or reload if this person changed")
			return
		}
		http.Redirect(w, r, "/family/"+url.PathEscape(id)+"?saved=1", http.StatusSeeOther)
		return
	default:
		methodNotAllowedFor(w, "GET, HEAD, POST")
		return
	}
	status := ""
	if r.URL.Query().Get("saved") == "1" {
		status = "Details saved."
	}
	if entry := healthEntryStatus(r.URL.Query().Get("entry")); entry != "" {
		status = entry
	}
	a.renderPerson(r.Context(), w, r, scope, csrf, person, status, "", http.StatusOK)
}

// renderPerson draws one person's page, with the baby log for a young child.
func (a *App) renderPerson(ctx context.Context, w http.ResponseWriter, r *http.Request, scope policy.ActorScope, csrf string, person people.Person, status, problem string, code int) {
	now := a.householdNow(ctx, scope)
	view := PersonView{Navigation: navigationForPath("/family/"), CSRF: csrf, Status: status, Relationships: relationshipOptions(person.Relationship)}
	view.Catalog = healthCatalogOptions()
	if log, err := a.healthRecords.CareLogFor(ctx, scope, person.Name, now.AddDate(0, 0, -6).Format("2006-01-02")); err == nil {
		view.Care = careView(log, now, false)
	}
	summary, err := a.healthRecords.Summarize(ctx, scope, health.AllRecords)
	if err == nil {
		view.Health = healthViewFor(summary, health.AllRecords, csrf, now, "all", person.Name)
		if view.Health.Person != person.Name {
			view.Health = HealthView{}
		}
	}
	view.Baby = a.loadBabyLog(ctx, scope, person, now, healthWeights(summary.Observations, person.Name))
	if view.Baby.Enabled {
		// The baby log's Weight list already holds these readings.
		view.Health.Series = withoutWeight(view.Health.Series)
	}
	if problem != "" {
		view.Baby.Error = problem
		if r.Method == http.MethodPost {
			view.Baby.ErrorCard, view.Baby.Values = babyErrorCard(r)
		}
	} else if view.Baby.Enabled {
		view.Baby.Undo, view.Status = a.babyUndo(ctx, scope, person, r.URL.Query(), view.Status)
	}
	view.Person = familyPersonView(person, now, len(view.Health.Records))
	rendered := newBufferedResponse(maxResponseBodyBytes)
	if err := a.templates.ExecuteTemplate(rendered, "person.html", view); err != nil || rendered.overflow {
		logRequestError(a.logger, ctx, "person_render_failed")
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if code != http.StatusOK {
		w.WriteHeader(code)
	}
	rendered.commit(w)
}
