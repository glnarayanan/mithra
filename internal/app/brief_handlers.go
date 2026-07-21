package app

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/glnarayanan/mithra/internal/coaching"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/providers"
)

type EvidenceView struct{ URL, Label string }
type CoachingItemView struct {
	Title, Copy, When, Meta, Reason, NextStep, EvidenceURL, EvidenceLabel string
	Evidence                                                              []EvidenceView
}
type CoachingHistoryView struct{ Generated, Model, Summary string }
type CoachingNudgeView struct {
	ID, Family, RecordID, Title, Copy, LensURL, LensLabel string
	FollowUpEnabled                                       bool
}
type BriefView struct {
	Navigation                                              []NavigationItem
	CSRF, Status, Freshness                                 string
	Stale, PersonalStale, HasRecords, HasShared, CanRefresh bool
	AIConfigured, Owner                                     bool
	Lead                                                    CoachingItemView
	Dates, Priorities, OnlyYou, Insights                    []CoachingItemView
	InsightHistory                                          []CoachingHistoryView
	InsightGenerated, InsightModel                          string
	Nudges                                                  []CoachingNudgeView
	Capture                                                 CaptureView
}
type WeekReviewView struct {
	Navigation                                           []NavigationItem
	CSRF, Status, Period, WeeklyStatus, WeeklyStatusCopy string
	PrivateStatus, PrivateStatusCopy                     string
	Stale, PrivateStale, CanRefresh                      bool
	Priorities, Progress, Soon, Later, OnlyYou           []CoachingItemView
	Observation                                          CoachingItemView
	AIInsights                                           []CoachingItemView
	InsightHistory                                       []CoachingHistoryView
	InsightGenerated, InsightModel                       string
	Nudges                                               []CoachingNudgeView
}

func (a *App) brief(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if !allowsRead(r.Method) {
		methodNotAllowed(w)
		return
	}
	scope, ok := a.sessionScope(r)
	if !ok {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	csrf := ""
	if cookie, err := r.Cookie(a.cookieName(csrfCookieName)); err == nil && len(cookie.Value) <= maxFormFieldBytes {
		csrf = cookie.Value
	}
	if r.Method == http.MethodHead {
		writeHTMLHead(w)
		return
	}
	status := ""
	if r.URL.Query().Get("captured") == "1" {
		status = "Update added. You can undo it for ten minutes from your recent captures."
	}
	a.renderBrief(r, w, scope, csrf, status)
}

func (a *App) weekReview(w http.ResponseWriter, r *http.Request) {
	if !allowsRead(r.Method) {
		methodNotAllowed(w)
		return
	}
	scope, ok := a.sessionScope(r)
	if !ok {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	csrf := ""
	if cookie, err := r.Cookie(a.cookieName(csrfCookieName)); err == nil && len(cookie.Value) <= maxFormFieldBytes {
		csrf = cookie.Value
	}
	if r.Method == http.MethodHead {
		writeHTMLHead(w)
		return
	}
	a.renderWeek(r, w, scope, csrf, "")
}

func (a *App) refreshBrief(w http.ResponseWriter, r *http.Request) { a.refreshCoaching(w, r, "brief") }
func (a *App) refreshWeek(w http.ResponseWriter, r *http.Request)  { a.refreshCoaching(w, r, "week") }

func (a *App) refreshCoaching(w http.ResponseWriter, r *http.Request, mode string) {
	if r.Method != http.MethodPost {
		methodNotAllowedFor(w, "POST")
		return
	}
	scope, csrf, ok := a.authenticated(r)
	if !ok {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	if !a.validSessionMutation(r, a.sessionCookie(r)) {
		a.renderCoachingMode(r, w, scope, csrf, mode, "We could not verify that refresh. Nothing was sent.")
		return
	}
	configured, _ := a.providerSettings.Configured(r.Context(), scope)
	if !configured {
		a.renderCoachingMode(r, w, scope, csrf, mode, "Connect a model provider in Settings before asking Mithra for a fresh view.")
		return
	}
	updated, failed := 0, 0
	updatedScopes, failedScopes := []string{}, []string{}
	for _, visibility := range []policy.Visibility{policy.Shared, policy.Personal} {
		input, err := a.coaching.BuildContext(r.Context(), scope, visibility)
		if err != nil {
			logRequestError(a.logger, r.Context(), "coaching_"+string(visibility)+"_context_failed")
			failed++
			failedScopes = append(failedScopes, coachingScopeLabel(visibility))
			continue
		}
		if len(input.Facts) == 0 {
			continue
		}
		callContext, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		output, model, err := a.analyzeCoaching(callContext, scope, mode, input)
		cancel()
		if err != nil {
			logRequestError(a.logger, r.Context(), "coaching_"+string(visibility)+"_provider_failed")
			failed++
			failedScopes = append(failedScopes, coachingScopeLabel(visibility))
			continue
		}
		if err = a.coaching.Publish(r.Context(), scope, mode, visibility, input, output, model); err != nil {
			logRequestError(a.logger, r.Context(), "coaching_"+string(visibility)+"_publish_"+coaching.PublishErrorCode(err))
			failed++
			failedScopes = append(failedScopes, coachingScopeLabel(visibility))
		} else {
			updated++
			updatedScopes = append(updatedScopes, coachingScopeLabel(visibility))
		}
	}
	a.ensureCoachingNudge(r.Context(), scope)
	if nudges, err := a.coaching.ListNudges(r.Context(), scope); err == nil {
		for _, nudge := range nudges {
			a.sendNudgeEmail(r.Context(), scope, nudge, true)
		}
	}
	status := "Mithra insights are up to date."
	if updated == 0 && failed > 0 {
		status = "Mithra could not refresh " + strings.Join(failedScopes, " or ") + " insights. Your saved information is still available."
	} else if failed > 0 {
		status = "Mithra refreshed " + strings.Join(updatedScopes, " and ") + ". It could not refresh " + strings.Join(failedScopes, " or ") + ". Your saved information is still available."
	} else if updated > 0 {
		status = "Mithra refreshed " + strings.Join(updatedScopes, " and ") + " insights."
	}
	a.renderCoachingMode(r, w, scope, csrf, mode, status)
}

func (a *App) updateNudge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowedFor(w, "POST")
		return
	}
	scope, csrf, ok := a.authenticated(r)
	if !ok {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	if !a.validSessionMutation(r, a.sessionCookie(r)) {
		a.renderBrief(r, w, scope, csrf, "We could not verify that update.")
		return
	}
	id := boundedField(r, "nudge_id", 64)
	action := boundedField(r, "nudge_action", 32)
	if err := a.coaching.UpdateNudge(r.Context(), scope, id, action); err != nil {
		a.renderBrief(r, w, scope, csrf, "That update is no longer waiting.")
		return
	}
	a.renderBrief(r, w, scope, csrf, "Reminder preference updated.")
}

func (a *App) renderCoachingMode(r *http.Request, w http.ResponseWriter, scope policy.ActorScope, csrf, mode, status string) {
	if mode == "week" {
		a.renderWeek(r, w, scope, csrf, status)
	} else {
		a.renderBrief(r, w, scope, csrf, status)
	}
}

func (a *App) renderBrief(r *http.Request, w http.ResponseWriter, scope policy.ActorScope, csrf, status string) {
	overview, err := a.coaching.Overview(r.Context(), scope, time.Now().UTC())
	if err != nil {
		logRequestError(a.logger, r.Context(), "brief_build_failed")
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	providerConfig, providerErr := a.providerSettings.ProviderDetails(r.Context(), scope)
	configured := providerErr == nil
	evidence := evidenceMap(overview.SharedContext, overview.PersonalContext)
	view := BriefView{Navigation: navigationForPath("/"), CSRF: csrf, Status: status, HasRecords: overview.HasRecords, HasShared: overview.Shared.Lead.Title != "", CanRefresh: configured && overview.HasRecords && csrf != "", AIConfigured: configured, Owner: scope.Role == "owner", Stale: overview.SharedCache.Stale, PersonalStale: overview.PersonalCache.Stale, Lead: itemView(overview.Shared.Lead, evidence), Insights: itemViews(overview.Shared.Insights, evidence), Dates: itemViews(overview.Shared.Dates, evidence), Priorities: itemViews(overview.Shared.Priorities, evidence), OnlyYou: itemViews(privateItems(overview.Personal), evidence), InsightHistory: historyViews(overview.SharedHistory), InsightGenerated: insightGenerated(overview.SharedCache), InsightModel: overview.SharedCache.Model, Capture: CaptureView{CSRF: csrf, ProviderConfigured: configured, VoiceSupported: configured && providerConfig.ProviderID == providers.ProviderOpenAI}}
	view.Freshness = freshness(overview.SharedCache, "Up to date")
	if view.Status == "" && view.Stale {
		view.Status = "A newer update is available. Dates and sources are still up to date."
	}
	view.Nudges = a.nudgeViews(r.Context(), scope, append(contextReviewFacts(overview.SharedContext), contextReviewFacts(overview.PersonalContext)...))
	a.renderTemplate(r.Context(), w, "brief.html", view)
}

func (a *App) renderWeek(r *http.Request, w http.ResponseWriter, scope policy.ActorScope, csrf, status string) {
	now := time.Now().UTC()
	review, err := a.coaching.Week(r.Context(), scope, now)
	if err != nil {
		logRequestError(a.logger, r.Context(), "week_build_failed")
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	configured, _ := a.providerSettings.Configured(r.Context(), scope)
	evidence := evidenceMap(review.Shared.Context, review.Personal.Context)
	from := now.AddDate(0, 0, -6)
	privateItems := reviewEventViews(append(append(review.Personal.Issues, review.Personal.Upcoming...), review.Personal.Changes...), evidence)
	privateItems = append(privateItems, itemViews(review.Personal.Insights, evidence)...)
	soon, later := splitReviewUpcoming(withoutPriorityEvents(review.Shared.Upcoming, review.Shared.Priorities), now)
	view := WeekReviewView{
		Navigation:        navigationForPath("/review"),
		CSRF:              csrf,
		Status:            status,
		Period:            from.Format("2 Jan") + " – " + now.Format("2 Jan 2006"),
		WeeklyStatus:      review.Shared.Status.Label,
		WeeklyStatusCopy:  review.Shared.Status.Copy,
		PrivateStatus:     review.Personal.Status.Label,
		PrivateStatusCopy: review.Personal.Status.Copy,
		Stale:             review.Shared.Cache.Stale,
		PrivateStale:      review.Personal.Cache.Stale,
		CanRefresh:        configured && review.HasRecords && csrf != "",
		Priorities:        reviewEventViews(review.Shared.Priorities, evidence),
		Progress:          reviewEventViews(review.Shared.Progress, evidence),
		Soon:              reviewEventViews(soon, evidence),
		Later:             reviewEventViews(later, evidence),
		OnlyYou:           privateItems,
		Observation:       itemView(review.Shared.Observation, evidence),
		InsightHistory:    historyViews(review.Shared.History),
		InsightGenerated:  insightGenerated(review.Shared.Cache),
		InsightModel:      review.Shared.Cache.Model,
	}
	if review.Shared.Cache.Found {
		view.AIInsights = itemViews(review.Shared.Insights, evidence)
	}
	if view.Status == "" && view.Stale {
		view.Status = "A newer update is available. Dates and sources are still up to date."
	}
	issues := append(append([]coaching.ReviewEvent(nil), review.Shared.Issues...), review.Personal.Issues...)
	view.Nudges = withoutReviewIssueNudges(a.nudgeViews(r.Context(), scope, append(review.Shared.Context.ReviewFacts, review.Personal.Context.ReviewFacts...)), issues)
	a.renderTemplate(r.Context(), w, "review.html", view)
}

func splitReviewUpcoming(events []coaching.ReviewEvent, now time.Time) ([]coaching.ReviewEvent, []coaching.ReviewEvent) {
	soon, later := []coaching.ReviewEvent{}, []coaching.ReviewEvent{}
	cutoff := now.UTC().AddDate(0, 0, 7).Format("2006-01-02")
	for _, event := range events {
		if event.When != "" && event.When <= cutoff {
			soon = append(soon, event)
		} else {
			later = append(later, event)
		}
	}
	return soon, later
}

func withoutPriorityEvents(events, priorities []coaching.ReviewEvent) []coaching.ReviewEvent {
	used := make(map[string]struct{}, len(priorities))
	for _, priority := range priorities {
		for _, evidenceID := range priority.EvidenceIDs {
			used[evidenceID] = struct{}{}
		}
	}
	out := make([]coaching.ReviewEvent, 0, len(events))
	for _, event := range events {
		duplicate := false
		for _, evidenceID := range event.EvidenceIDs {
			if _, exists := used[evidenceID]; exists {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, event)
		}
	}
	return out
}

func withoutReviewIssueNudges(nudges []CoachingNudgeView, issues []coaching.ReviewEvent) []CoachingNudgeView {
	blocked := make(map[string]struct{}, len(issues))
	for _, issue := range issues {
		for _, fact := range issue.Facts {
			blocked[fact.Family+"\x00"+fact.RecordID] = struct{}{}
		}
	}
	out := make([]CoachingNudgeView, 0, len(nudges))
	for _, nudge := range nudges {
		if _, exists := blocked[nudge.Family+"\x00"+nudge.RecordID]; !exists {
			out = append(out, nudge)
		}
	}
	return out
}

func evidenceMap(contexts ...coaching.Context) map[string]coaching.Fact {
	out := map[string]coaching.Fact{}
	for _, c := range contexts {
		for _, f := range contextReviewFacts(c) {
			out[f.EvidenceID] = f
		}
	}
	return out
}

func contextReviewFacts(context coaching.Context) []coaching.Fact {
	if context.ReviewFacts != nil {
		return context.ReviewFacts
	}
	return context.Facts
}
func itemView(item coaching.Item, evidence map[string]coaching.Fact) CoachingItemView {
	view := CoachingItemView{Title: item.Title, Copy: item.Copy, When: item.When}
	seen := map[string]struct{}{}
	for _, evidenceID := range item.EvidenceIDs {
		if fact, ok := evidence[evidenceID]; ok && fact.SourceID != "" {
			url := sourceURL(fact.SourceID)
			if _, exists := seen[url]; exists {
				continue
			}
			seen[url] = struct{}{}
			view.Evidence = append(view.Evidence, EvidenceView{URL: url, Label: "Source"})
		}
	}
	if len(view.Evidence) > 0 {
		view.EvidenceURL, view.EvidenceLabel = view.Evidence[0].URL, "Details"
	}
	return view
}
func itemViews(items []coaching.Item, evidence map[string]coaching.Fact) []CoachingItemView {
	out := make([]CoachingItemView, 0, len(items))
	for _, item := range items {
		out = append(out, itemView(item, evidence))
	}
	return out
}
func reviewEventViews(events []coaching.ReviewEvent, evidence map[string]coaching.Fact) []CoachingItemView {
	out := make([]CoachingItemView, 0, len(events))
	for _, event := range events {
		view := CoachingItemView{Title: event.Title, Copy: event.Copy, When: reviewDate(event.When), Reason: event.Reason, NextStep: event.NextStep, Meta: reviewEventMeta(event)}
		seen := map[string]struct{}{}
		for _, evidenceID := range event.EvidenceIDs {
			fact, ok := evidence[evidenceID]
			if !ok || fact.SourceID == "" {
				continue
			}
			url := sourceURL(fact.SourceID)
			if _, exists := seen[url]; exists {
				continue
			}
			seen[url] = struct{}{}
			view.Evidence = append(view.Evidence, EvidenceView{URL: url, Label: "Source"})
		}
		if len(view.Evidence) > 0 {
			view.EvidenceURL, view.EvidenceLabel = view.Evidence[0].URL, "Details"
		}
		out = append(out, view)
	}
	return out
}

func reviewEventMeta(event coaching.ReviewEvent) string {
	parts := make([]string, 0, 5)
	if event.When != "" {
		parts = append(parts, reviewDate(event.When))
	}
	if event.Time != "" {
		parts = append(parts, event.Time)
	}
	for _, value := range []string{event.Domain, event.Visibility, event.Status} {
		if value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " · ")
}
func reviewDate(value string) string {
	date, err := time.Parse("2006-01-02", value)
	if err != nil {
		return value
	}
	return date.Format("2 Jan 2006")
}
func historyViews(history []coaching.History) []CoachingHistoryView {
	out := make([]CoachingHistoryView, 0, len(history))
	for _, item := range history {
		summary := item.Narrative.Lead.Title
		if summary == "" && len(item.Narrative.Insights) > 0 {
			summary = item.Narrative.Insights[0].Title
		}
		out = append(out, CoachingHistoryView{Generated: item.GeneratedAt.Local().Format("2 Jan, 15:04"), Model: item.Model, Summary: summary})
	}
	return out
}
func insightGenerated(state coaching.CacheState) string {
	if !state.Found || state.GeneratedAt.IsZero() {
		return ""
	}
	return "Generated " + state.GeneratedAt.Local().Format("2 Jan, 15:04")
}
func coachingScopeLabel(visibility policy.Visibility) string {
	if visibility == policy.Personal {
		return "Only you"
	}
	return "shared"
}
func privateItems(n coaching.Narrative) []coaching.Item {
	seen := make(map[string]struct{})
	out := make([]coaching.Item, 0, 6)
	for _, items := range [][]coaching.Item{n.Insights, n.Inconsistencies, n.Dates, n.Changes} {
		for _, item := range items {
			key := strings.Join(item.EvidenceIDs, "\x00")
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, item)
			if len(out) == 6 {
				return out
			}
		}
	}
	return out
}
func freshness(state coaching.CacheState, fallback string) string {
	if !state.Found {
		return fallback
	}
	return "Updated " + state.GeneratedAt.Local().Format("2 Jan, 15:04")
}

func (a *App) ensureCoachingNudge(ctx context.Context, scope policy.ActorScope) {
	for _, visibility := range []policy.Visibility{policy.Personal, policy.Shared} {
		input, err := a.coaching.BuildContext(ctx, scope, visibility)
		if err != nil {
			continue
		}
		facts := input.ReviewFacts
		if facts == nil {
			facts = input.Facts
		}
		for _, fact := range facts {
			if strings.TrimSpace(fact.Issue) == "" {
				continue
			}
			nudge, err := a.coaching.EnsureNudge(ctx, scope, fact.Family, fact.RecordID, fact.SourceID)
			if err == nil {
				a.sendNudgeEmail(ctx, scope, nudge, false)
			}
			return
		}
	}
}

func (a *App) sendNudgeEmail(ctx context.Context, scope policy.ActorScope, nudge coaching.Nudge, followUp bool) {
	if (!followUp && nudge.InitialEmailSent) || (followUp && (!nudge.FollowUpEnabled || nudge.FollowUpEmailSent)) {
		return
	}
	var email string
	if a.db.QueryRowContext(ctx, `SELECT email FROM users WHERE id=? AND status='active'`, scope.ActorID).Scan(&email) != nil {
		return
	}
	subject := "Mithra has a household update"
	text := "A household item is waiting for your update. Open Mithra: " + a.origin.String() + "/"
	action := "initial-email-sent"
	if followUp {
		subject = "Mithra household update follow-up"
		text = "The household item you chose to follow up on is still waiting. Open Mithra: " + a.origin.String() + "/"
		action = "follow-up-email-sent"
	}
	if a.mailer.Send(ctx, providers.Message{To: email, Subject: subject, Text: text}) == nil {
		_ = a.coaching.UpdateNudge(ctx, scope, nudge.ID, action)
	}
}

func (a *App) nudgeViews(ctx context.Context, scope policy.ActorScope, facts []coaching.Fact) []CoachingNudgeView {
	nudges, err := a.coaching.ListNudges(ctx, scope)
	if err != nil {
		return nil
	}
	byRecord := map[string]coaching.Fact{}
	for _, fact := range facts {
		byRecord[fact.Family+"\x00"+fact.RecordID] = fact
	}
	out := make([]CoachingNudgeView, 0, len(nudges))
	for _, n := range nudges {
		fact, ok := byRecord[n.Family+"\x00"+n.RecordID]
		if !ok {
			continue
		}
		out = append(out, CoachingNudgeView{ID: n.ID, Family: n.Family, RecordID: n.RecordID, Title: fact.Content, Copy: "Add an update when you have one, or mark this as reviewed.", LensURL: "/" + n.Family, LensLabel: strings.Title(n.Family), FollowUpEnabled: n.FollowUpEnabled})
	}
	return out
}
