package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/glnarayanan/mithra/internal/auth"
	"github.com/glnarayanan/mithra/internal/household"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/providers"
)

const (
	sessionCookieName    = "mithra_session"
	csrfCookieName       = "mithra_csrf"
	resetCookieName      = "mithra_reset"
	invitationCookieName = "mithra_invitation"
	maxFormFieldBytes    = 1024
	bootstrapLifetime    = 10 * time.Minute
	invitationLifetime   = 7 * 24 * time.Hour
)

type unavailableMailer struct{}

func (unavailableMailer) Send(context.Context, providers.Message) error { return providers.ErrDelivery }

type AuthView struct {
	Title, Heading, Copy, Status, Error, Action, CSRF string
	Password                                          bool
}

type SettingsView struct {
	Members []household.Member
	Owner   bool
	CSRF    string
	Status  string
	Error   string
}

func canonicalOrigin(raw string, secure bool) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		if secure {
			return nil, errors.New("canonical origin is required when secure cookies are enabled")
		}
		raw = "http://127.0.0.1:8090"
	}
	origin, err := url.Parse(raw)
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" {
		return nil, errors.New("canonical origin must be an absolute origin without a path")
	}
	if origin.Scheme != "https" && origin.Scheme != "http" {
		return nil, errors.New("canonical origin must use HTTP or HTTPS")
	}
	if secure && origin.Scheme != "https" {
		return nil, errors.New("secure cookies require an HTTPS canonical origin")
	}
	if origin.Scheme == "http" {
		ip := net.ParseIP(origin.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return nil, errors.New("HTTP canonical origin is allowed only for literal loopback development")
		}
	}
	return origin, nil
}

func normalizeAllowlist(values []string) ([]string, error) {
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		for _, email := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
			normalized, err := household.NormalizeEmail(email)
			if err != nil {
				return nil, errors.New("allowlist contains an invalid email")
			}
			unique[normalized] = struct{}{}
		}
	}
	result := make([]string, 0, len(unique))
	for email := range unique {
		result = append(result, email)
	}
	return result, nil
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.renderAuth(r.Context(), w, AuthView{Title: "Sign in", Heading: "Welcome back", Copy: "Sign in to your household."})
	case http.MethodHead:
		writeHTMLHead(w)
	case http.MethodPost:
		if !a.validPublicMutation(r) {
			a.renderAuth(r.Context(), w, AuthView{Title: "Sign in", Heading: "Welcome back", Error: "We could not verify that request. Please try again."})
			return
		}
		email, password, ok := formFields(r, "email", "password")
		if !ok {
			a.renderAuth(r.Context(), w, AuthView{Title: "Sign in", Heading: "Welcome back", Error: "Enter your email and password."})
			return
		}
		session, err := a.auth.Login(r.Context(), email, password, a.clientThrottleKey(r))
		if err != nil {
			a.renderAuth(r.Context(), w, AuthView{Title: "Sign in", Heading: "Welcome back", Error: "That email or password did not work. Try again or request a password link."})
			return
		}
		a.setSession(w, session)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	default:
		methodNotAllowedFor(w, "GET, HEAD, POST")
	}
}

func (a *App) forgotPassword(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.renderAuth(r.Context(), w, AuthView{Title: "Set a password", Heading: "Set or reset your password", Copy: "Enter the email on your household allowlist. If it is eligible, we will send a link."})
	case http.MethodHead:
		writeHTMLHead(w)
	case http.MethodPost:
		if !a.validPublicMutation(r) {
			a.renderAuth(r.Context(), w, AuthView{Title: "Set a password", Heading: "Check your email", Status: resetAcknowledgement})
			return
		}
		email, _, _ := formFields(r, "email")
		delivery, err := a.auth.RequestPasswordReset(r.Context(), email, a.clientThrottleKey(r))
		if err == nil && delivery != nil {
			link := a.canonicalLink("/auth/reset", "token", delivery.Token)
			if sendErr := a.mailer.Send(r.Context(), providers.Message{To: delivery.Email, Subject: "Your Mithra password link", Text: "Use this Mithra link within one hour:\n" + link + "\n\nIf you did not request it, you can ignore this email."}); sendErr != nil {
				logRequestError(a.logger, r.Context(), "password_delivery_failed")
			}
		}
		a.renderAuth(r.Context(), w, AuthView{Title: "Set a password", Heading: "Check your email", Status: resetAcknowledgement})
	default:
		methodNotAllowedFor(w, "GET, HEAD, POST")
	}
}

const resetAcknowledgement = "If that email can use Mithra, a password link is on its way. Check your inbox and spam folder."

func (a *App) bootstrapReset(w http.ResponseWriter, r *http.Request) {
	if !allowsRead(r.Method) {
		methodNotAllowed(w)
		return
	}
	if r.Method == http.MethodGet {
		if token := boundedQuery(r, "token"); token != "" {
			a.setBootstrap(w, resetCookieName, token)
		}
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, "/auth/password", http.StatusSeeOther)
}

func (a *App) bootstrapInvitation(w http.ResponseWriter, r *http.Request) {
	if !allowsRead(r.Method) {
		methodNotAllowed(w)
		return
	}
	if r.Method == http.MethodGet {
		if token := boundedQuery(r, "token"); token != "" {
			a.setBootstrap(w, invitationCookieName, token)
		}
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, "/auth/password", http.StatusSeeOther)
}

func (a *App) passwordSetup(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.renderAuth(r.Context(), w, AuthView{Title: "Choose a password", Heading: "Choose a password", Copy: "Use at least 12 characters. This link does not become used until you save your password.", Password: true})
	case http.MethodHead:
		writeHTMLHead(w)
	case http.MethodPost:
		if !a.validPublicMutation(r) {
			a.renderAuth(r.Context(), w, AuthView{Title: "Choose a password", Heading: "Choose a password", Error: "We could not verify that request. Please open the link again.", Password: true})
			return
		}
		password, _, ok := formFields(r, "password")
		reset, _ := r.Cookie(a.cookieName(resetCookieName))
		invitation, _ := r.Cookie(a.cookieName(invitationCookieName))
		if !ok || !validBootstrapCookie(reset) && !validBootstrapCookie(invitation) {
			a.renderAuth(r.Context(), w, AuthView{Title: "Choose a password", Heading: "Choose a password", Error: "This password link is invalid or expired. Request a new one.", Password: true})
			return
		}
		resetToken, invitationToken := "", ""
		if validBootstrapCookie(reset) {
			resetToken = reset.Value
		}
		if validBootstrapCookie(invitation) {
			invitationToken = invitation.Value
		}
		session, err := a.auth.SetPassword(r.Context(), resetToken, password, invitationToken)
		if err != nil {
			a.renderAuth(r.Context(), w, AuthView{Title: "Choose a password", Heading: "Choose a password", Error: passwordError(err), Password: true})
			return
		}
		a.clearBootstrap(w)
		a.setSession(w, session)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	default:
		methodNotAllowedFor(w, "GET, HEAD, POST")
	}
}

func validBootstrapCookie(cookie *http.Cookie) bool {
	return cookie != nil && cookie.Value != "" && len(cookie.Value) <= maxFormFieldBytes
}

func passwordError(err error) string {
	if errors.Is(err, auth.ErrPassword) {
		return "Choose a password between 12 and 128 characters."
	}
	if errors.Is(err, household.ErrAdultLimit) {
		return "That household already has two adults. Ask its owner for a new invitation if needed."
	}
	return "This password or invitation link is invalid or expired. Request a new password link."
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowedFor(w, "POST")
		return
	}
	cookie := a.sessionCookie(r)
	if !a.validSessionMutation(r, cookie) {
		a.clearSession(w)
		writeError(w, http.StatusForbidden, "request verification failed")
		return
	}
	if err := a.auth.RevokeSession(r.Context(), cookie); err != nil {
		logRequestError(a.logger, r.Context(), "logout_failed")
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	a.clearSession(w)
	http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
}

func (a *App) settings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
		methodNotAllowedFor(w, "GET, HEAD, POST")
		return
	}
	scope, csrf, ok := a.authenticated(r)
	if !ok {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.renderSettings(r.Context(), w, scope, csrf, "", "")
	case http.MethodHead:
		writeHTMLHead(w)
	case http.MethodPost:
		if !a.validSessionMutation(r, a.sessionCookie(r)) {
			a.renderSettings(r.Context(), w, scope, csrf, "", "We could not verify that request. Please try again.")
			return
		}
		email, _, ok := formFields(r, "email")
		if !ok {
			a.renderSettings(r.Context(), w, scope, csrf, "", "Enter an allowlisted email address.")
			return
		}
		invitation, err := a.auth.CreateInvitation(r.Context(), scope, email, invitationLifetime)
		if err != nil {
			a.renderSettings(r.Context(), w, scope, csrf, "", inviteError(err))
			return
		}
		link := a.canonicalLink("/auth/invitation", "token", invitation.Token)
		if err := a.mailer.Send(r.Context(), providers.Message{To: email, Subject: "You have been invited to Mithra", Text: "Join this Mithra household within seven days:\n" + link}); err != nil {
			logRequestError(a.logger, r.Context(), "invitation_delivery_failed")
			a.renderSettings(r.Context(), w, scope, csrf, "", "The invitation could not be delivered. Try again later.")
			return
		}
		a.renderSettings(r.Context(), w, scope, csrf, "Invitation sent. Your partner can choose a password from the secure link.", "")
	}
}

func inviteError(err error) string {
	switch {
	case errors.Is(err, household.ErrNotOwner):
		return "Only the household owner can invite a partner."
	case errors.Is(err, household.ErrAlreadyBound):
		return "That account already belongs to a household."
	case errors.Is(err, household.ErrAdultLimit):
		return "This household already has two adults."
	default:
		return "That invitation is not available. Check that the email is allowlisted and unassigned."
	}
}

func (a *App) authenticated(r *http.Request) (policy.ActorScope, string, bool) {
	scope, ok := a.sessionScope(r)
	if !ok {
		return policy.ActorScope{}, "", false
	}
	csrf, err := r.Cookie(a.cookieName(csrfCookieName))
	if err != nil || len(csrf.Value) > maxFormFieldBytes {
		return policy.ActorScope{}, "", false
	}
	return scope, csrf.Value, true
}

func (a *App) sessionScope(r *http.Request) (policy.ActorScope, bool) {
	cookie := a.sessionCookie(r)
	if cookie == "" {
		return policy.ActorScope{}, false
	}
	scope, err := a.auth.Authenticate(r.Context(), cookie)
	if err != nil {
		return policy.ActorScope{}, false
	}
	return scope, true
}

func (a *App) validSessionMutation(r *http.Request, cookie string) bool {
	if !a.validPublicMutation(r) || cookie == "" {
		return false
	}
	csrfCookie, err := r.Cookie(a.cookieName(csrfCookieName))
	if err != nil {
		return false
	}
	csrf := r.FormValue("csrf")
	if len(csrf) == 0 || len(csrf) > maxFormFieldBytes || csrf != csrfCookie.Value {
		return false
	}
	return a.auth.VerifyCSRF(r.Context(), cookie, csrf) == nil
}

func (a *App) validPublicMutation(r *http.Request) bool {
	if r.Method != http.MethodPost || !sameOrigin(r, a.origin) {
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "none" {
		return false
	}
	return true
}

func sameOrigin(r *http.Request, origin *url.URL) bool {
	seen := false
	for _, raw := range []string{r.Header.Get("Origin"), r.Header.Get("Referer")} {
		if raw == "" {
			continue
		}
		seen = true
		candidate, err := url.Parse(raw)
		if err != nil || candidate.Scheme != origin.Scheme || candidate.Host != origin.Host {
			return false
		}
	}
	return seen
}

func formFields(r *http.Request, names ...string) (string, string, bool) {
	if err := r.ParseForm(); err != nil {
		return "", "", false
	}
	var values [2]string
	for index, name := range names {
		value := r.PostForm.Get(name)
		if len(value) > maxFormFieldBytes {
			return "", "", false
		}
		if index < len(values) {
			values[index] = value
		}
	}
	return values[0], values[1], true
}

func boundedQuery(r *http.Request, key string) string {
	value := r.URL.Query().Get(key)
	if len(value) == 0 || len(value) > maxFormFieldBytes {
		return ""
	}
	return value
}

func (a *App) clientThrottleKey(r *http.Request) string {
	if a.trustedProxy {
		forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
		if strings.Contains(forwarded, ",") {
			return "shared-proxy"
		}
		if ip := net.ParseIP(forwarded); ip != nil {
			return opaqueThrottleKey(ip.String())
		}
		return "shared-proxy"
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(host) == nil {
		return "unknown"
	}
	return opaqueThrottleKey(net.ParseIP(host).String())
}

func opaqueThrottleKey(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", digest[:])
}

func (a *App) canonicalLink(path, key, token string) string {
	link := *a.origin
	link.Path = path
	query := url.Values{}
	query.Set(key, token)
	link.RawQuery = query.Encode()
	return link.String()
}

func (a *App) setSession(w http.ResponseWriter, session auth.Session) {
	expires := session.ExpiresAt
	http.SetCookie(w, a.cookie(sessionCookieName, session.Cookie, expires, http.SameSiteLaxMode))
	http.SetCookie(w, a.cookie(csrfCookieName, session.CSRF, expires, http.SameSiteStrictMode))
}

func (a *App) setBootstrap(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, a.cookie(name, value, time.Now().Add(bootstrapLifetime), http.SameSiteLaxMode))
}

func (a *App) clearBootstrap(w http.ResponseWriter) {
	a.clearCookie(w, resetCookieName)
	a.clearCookie(w, invitationCookieName)
}

func (a *App) clearSession(w http.ResponseWriter) {
	a.clearCookie(w, sessionCookieName)
	a.clearCookie(w, csrfCookieName)
}

func (a *App) clearCookie(w http.ResponseWriter, name string) {
	cookie := a.cookie(name, "", time.Unix(1, 0), http.SameSiteLaxMode)
	cookie.MaxAge = -1
	http.SetCookie(w, cookie)
}

func (a *App) cookie(name, value string, expires time.Time, sameSite http.SameSite) *http.Cookie {
	return &http.Cookie{Name: a.cookieName(name), Value: value, Path: "/", Expires: expires, HttpOnly: true, Secure: a.secure, SameSite: sameSite}
}

func (a *App) cookieName(name string) string {
	if a.secure {
		return "__Host-" + name
	}
	return name
}

func (a *App) sessionCookie(r *http.Request) string {
	cookie, err := r.Cookie(a.cookieName(sessionCookieName))
	if err != nil || len(cookie.Value) > maxFormFieldBytes {
		return ""
	}
	return cookie.Value
}

func (a *App) renderAuth(ctx context.Context, w http.ResponseWriter, view AuthView) {
	if view.Action == "" {
		switch view.Title {
		case "Sign in":
			view.Action = "/auth/login"
		case "Choose a password":
			view.Action = "/auth/password"
		default:
			view.Action = "/auth/forgot-password"
		}
	}
	a.renderTemplate(ctx, w, "auth.html", view)
}

func (a *App) renderSettings(ctx context.Context, w http.ResponseWriter, scope policy.ActorScope, csrf, status, problem string) {
	members, err := a.auth.Members(ctx, scope)
	if err != nil {
		logRequestError(a.logger, ctx, "settings_members_failed")
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	a.renderTemplate(ctx, w, "settings.html", SettingsView{Members: members, Owner: scope.Role == "owner", CSRF: csrf, Status: status, Error: problem})
}

func (a *App) renderTemplate(ctx context.Context, w http.ResponseWriter, name string, view any) {
	rendered := newBufferedResponse(maxResponseBodyBytes)
	if err := a.templates.ExecuteTemplate(rendered, name, view); err != nil || rendered.overflow {
		logRequestError(a.logger, ctx, "render_failed")
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	rendered.commit(w)
}

func writeHTMLHead(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
}
