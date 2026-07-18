package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/auth"
	"github.com/glnarayanan/mithra/internal/providers"
)

const testOrigin = "https://mithra.test"

func TestPasswordBootstrapIsOpaqueOneUseAndReplacesSessions(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	first := activate(t, application, mailer, "owner@example.com", "a first secure password", nil)

	forgot := authForm(http.MethodPost, "/auth/forgot-password", url.Values{"email": {"owner@example.com"}}, nil)
	forgot.Host = "hostile.example"
	forgot.Header.Set("X-Forwarded-Host", "hostile.example")
	forgotResponse := serve(application, forgot)
	if forgotResponse.Code != http.StatusOK || !strings.Contains(forgotResponse.Body.String(), resetAcknowledgement) {
		t.Fatalf("forgot response = %d %q", forgotResponse.Code, forgotResponse.Body.String())
	}
	message := mailer.last(t)
	resetURL, err := url.Parse(lastLine(message.Text))
	if err != nil || resetURL.Scheme+"://"+resetURL.Host != testOrigin {
		t.Fatalf("reset link used request-controlled origin: %q", message.Text)
	}
	token := resetURL.Query().Get("token")
	if token == "" || strings.Contains(forgotResponse.Body.String(), token) {
		t.Fatal("reset token leaked in the acknowledgement")
	}
	var persisted int
	if err := application.db.QueryRow(`SELECT COUNT(*) FROM password_reset_tokens WHERE token_hash=?`, token).Scan(&persisted); err != nil || persisted != 0 {
		t.Fatalf("raw reset token persisted: %d, %v", persisted, err)
	}

	bootstrap := serve(application, httptest.NewRequest(http.MethodGet, resetURL.RequestURI(), nil))
	if bootstrap.Code != http.StatusSeeOther || bootstrap.Header().Get("Location") != "/auth/password" || bootstrap.Header().Get("Referrer-Policy") != "no-referrer" || strings.Contains(bootstrap.Body.String(), token) {
		t.Fatalf("bootstrap response leaked or retained token: %d %q %q", bootstrap.Code, bootstrap.Header().Get("Location"), bootstrap.Body.String())
	}
	resetCookie := responseCookie(t, bootstrap, application.cookieName(resetCookieName))
	assertHostCookie(t, resetCookie, http.SameSiteLaxMode)
	var unconsumed int
	if err := application.db.QueryRow(`SELECT COUNT(*) FROM password_reset_tokens WHERE used_at IS NULL AND revoked_at IS NULL`).Scan(&unconsumed); err != nil || unconsumed != 1 {
		t.Fatalf("reset GET consumed token: unconsumed=%d err=%v", unconsumed, err)
	}

	passwordPage := httptest.NewRequest(http.MethodGet, "/auth/password", nil)
	passwordPage.AddCookie(resetCookie)
	passwordResponse := serve(application, passwordPage)
	if passwordResponse.Code != http.StatusOK || strings.Contains(passwordResponse.Body.String(), token) || !strings.Contains(passwordResponse.Body.String(), `autocomplete="new-password"`) {
		t.Fatalf("password page exposed bootstrap token or is incomplete: %d %q", passwordResponse.Code, passwordResponse.Body.String())
	}

	setup := authForm(http.MethodPost, "/auth/password", url.Values{"password": {"a changed secure password"}}, []*http.Cookie{resetCookie, first.session, first.csrf})
	setupResponse := serve(application, setup)
	if setupResponse.Code != http.StatusSeeOther || setupResponse.Header().Get("Location") != "/" {
		t.Fatalf("password setup = %d %q", setupResponse.Code, setupResponse.Body.String())
	}
	secondSession := responseCookie(t, setupResponse, application.cookieName(sessionCookieName))
	secondCSRF := responseCookie(t, setupResponse, application.cookieName(csrfCookieName))
	assertHostCookie(t, secondSession, http.SameSiteLaxMode)
	assertHostCookie(t, secondCSRF, http.SameSiteStrictMode)
	if secondSession.Value == first.session.Value || secondCSRF.Value == first.csrf.Value {
		t.Fatal("password setup did not replace browser secrets")
	}

	oldShell := httptest.NewRequest(http.MethodGet, "/", nil)
	oldShell.AddCookie(first.session)
	if old := serve(application, oldShell); old.Code != http.StatusSeeOther || old.Header().Get("Location") != "/auth/login" {
		t.Fatalf("old session remained active: %d %q", old.Code, old.Header().Get("Location"))
	}
	newShell := httptest.NewRequest(http.MethodGet, "/", nil)
	newShell.AddCookie(secondSession)
	if current := serve(application, newShell); current.Code != http.StatusOK || strings.Contains(current.Body.String(), token) {
		t.Fatalf("new session did not reach the protected shell safely: %d", current.Code)
	}

	reused := authForm(http.MethodPost, "/auth/password", url.Values{"password": {"another changed password"}}, []*http.Cookie{resetCookie})
	if response := serve(application, reused); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "invalid or expired") {
		t.Fatalf("reused reset link response = %d %q", response.Code, response.Body.String())
	}
}

func TestResetAcknowledgementsAndRequestValidationDoNotRevealAccounts(t *testing.T) {
	application, mailer := newAuthTestApp(t, "allowed@example.com")
	request := func(email, origin, site string) *httptest.ResponseRecorder {
		r := authForm(http.MethodPost, "/auth/forgot-password", url.Values{"email": {email}}, nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		r.Header.Set("Sec-Fetch-Site", site)
		return serve(application, r)
	}

	allowed := request("allowed@example.com", testOrigin, "same-origin")
	unknown := request("unknown@example.com", testOrigin, "same-origin")
	if allowed.Code != http.StatusOK || unknown.Code != http.StatusOK || allowed.Body.String() != unknown.Body.String() {
		t.Fatalf("reset acknowledgement differs: allowed=%d/%q unknown=%d/%q", allowed.Code, allowed.Body.String(), unknown.Code, unknown.Body.String())
	}
	if len(mailer.messages) != 1 {
		t.Fatalf("reset delivery count = %d, want one", len(mailer.messages))
	}
	if err := application.auth.SynchronizeAllowlist(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	disabled := request("allowed@example.com", testOrigin, "same-origin")
	if disabled.Body.String() != allowed.Body.String() {
		t.Fatalf("disabled acknowledgement = %q, want %q", disabled.Body.String(), allowed.Body.String())
	}
	crossSite := request("unknown@example.com", testOrigin, "cross-site")
	if crossSite.Body.String() != allowed.Body.String() || len(mailer.messages) != 1 {
		t.Fatalf("cross-site reset revealed state or sent mail: %q messages=%d", crossSite.Body.String(), len(mailer.messages))
	}
	for range 6 {
		throttled := request("unknown@example.com", testOrigin, "same-origin")
		if throttled.Body.String() != allowed.Body.String() {
			t.Fatalf("throttled acknowledgement = %q, want %q", throttled.Body.String(), allowed.Body.String())
		}
	}

	noOrigin := authForm(http.MethodPost, "/auth/login", url.Values{"email": {"allowed@example.com"}, "password": {"a secure password"}}, nil)
	noOrigin.Header.Del("Origin")
	noOrigin.Header.Set("Referer", testOrigin+"/auth/login")
	if response := serve(application, noOrigin); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "did not work") {
		t.Fatalf("disabled login error response = %d %q", response.Code, response.Body.String())
	}

	for _, method := range []struct{ path, want string }{{"/auth/login", "GET, HEAD, POST"}, {"/auth/logout", "POST"}, {"/settings", "GET, HEAD, POST"}} {
		r := httptest.NewRequest(http.MethodDelete, method.path, nil)
		if response := serve(application, r); response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != method.want {
			t.Fatalf("%s Allow = %d %q, want 405 %q", method.path, response.Code, response.Header().Get("Allow"), method.want)
		}
	}
	for _, path := range []string{"/auth/login", "/auth/forgot-password", "/auth/password"} {
		if response := serve(application, httptest.NewRequest(http.MethodHead, path, nil)); response.Code != http.StatusOK || response.Body.Len() != 0 {
			t.Fatalf("HEAD %s = %d body=%q", path, response.Code, response.Body.String())
		}
	}
}

func TestSettingsInvitationCSRFAndHouseholdIsolation(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com", "partner@example.com", "third@example.com", "other@example.com")
	owner := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	other := activate(t, application, mailer, "other@example.com", "another owner password", nil)

	settings := httptest.NewRequest(http.MethodGet, "/settings", nil)
	settings.AddCookie(owner.session)
	settings.AddCookie(owner.csrf)
	settingsResponse := serve(application, settings)
	if settingsResponse.Code != http.StatusOK || !strings.Contains(settingsResponse.Body.String(), "owner@example.com") || strings.Contains(settingsResponse.Body.String(), "other@example.com") || !strings.Contains(settingsResponse.Body.String(), `name="csrf"`) {
		t.Fatalf("owner settings leaked a foreign household or missed csrf: %d %q", settingsResponse.Code, settingsResponse.Body.String())
	}
	otherSettings := httptest.NewRequest(http.MethodGet, "/settings", nil)
	otherSettings.AddCookie(other.session)
	otherSettings.AddCookie(other.csrf)
	if response := serve(application, otherSettings); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "other@example.com") || strings.Contains(response.Body.String(), "owner@example.com") {
		t.Fatalf("other household settings are not isolated: %d %q", response.Code, response.Body.String())
	}

	invalidCSRF := authForm(http.MethodPost, "/settings", url.Values{"email": {"partner@example.com"}, "csrf": {"wrong"}}, []*http.Cookie{owner.session, owner.csrf})
	if response := serve(application, invalidCSRF); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "could not verify") || len(mailer.messages) != 2 {
		t.Fatalf("invalid settings csrf = %d mail=%d body=%q", response.Code, len(mailer.messages), response.Body.String())
	}

	invite := settingsPost(owner, "partner@example.com")
	inviteResponse := serve(application, invite)
	if inviteResponse.Code != http.StatusOK || !strings.Contains(inviteResponse.Body.String(), "Invitation sent") {
		t.Fatalf("owner invitation = %d %q", inviteResponse.Code, inviteResponse.Body.String())
	}
	inviteToken := tokenFromMessage(t, mailer.last(t), "token")
	partner := activateInvitation(t, application, "a partner secure password", bootstrapInvitation(t, application, inviteToken))
	reused := authForm(http.MethodPost, "/auth/password", url.Values{"password": {"another partner password"}}, []*http.Cookie{bootstrapInvitation(t, application, inviteToken)})
	if response := serve(application, reused); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "invalid or expired") {
		t.Fatalf("reused invitation = %d %q", response.Code, response.Body.String())
	}

	nonOwner := settingsPost(partner, "third@example.com")
	if response := serve(application, nonOwner); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Only the household owner") {
		t.Fatalf("non-owner invitation = %d %q", response.Code, response.Body.String())
	}
	third := settingsPost(owner, "third@example.com")
	if response := serve(application, third); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "already has two adults") {
		t.Fatalf("third-adult invitation = %d %q", response.Code, response.Body.String())
	}

	badLogout := authForm(http.MethodPost, "/auth/logout", url.Values{"csrf": {owner.csrf.Value}}, []*http.Cookie{owner.session, owner.csrf})
	badLogout.Header.Set("Origin", "https://evil.example")
	if response := serve(application, badLogout); response.Code != http.StatusForbidden || !hasClearedCookie(response, application.cookieName(sessionCookieName)) {
		t.Fatalf("cross-origin logout = %d, cleared=%v", response.Code, hasClearedCookie(response, application.cookieName(sessionCookieName)))
	}
	validLogout := authForm(http.MethodPost, "/auth/logout", url.Values{"csrf": {partner.csrf.Value}}, []*http.Cookie{partner.session, partner.csrf})
	if response := serve(application, validLogout); response.Code != http.StatusSeeOther || !hasClearedCookie(response, application.cookieName(sessionCookieName)) || !hasClearedCookie(response, application.cookieName(csrfCookieName)) {
		t.Fatalf("logout = %d session-cleared=%v csrf-cleared=%v", response.Code, hasClearedCookie(response, application.cookieName(sessionCookieName)), hasClearedCookie(response, application.cookieName(csrfCookieName)))
	}

	if err := application.auth.SynchronizeAllowlist(context.Background(), []string{"partner@example.com", "third@example.com", "other@example.com"}); err != nil {
		t.Fatal(err)
	}
	removed := httptest.NewRequest(http.MethodGet, "/planning", nil)
	removed.AddCookie(owner.session)
	if response := serve(application, removed); response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/auth/login" {
		t.Fatalf("removed owner retained protected route access: %d %q", response.Code, response.Header().Get("Location"))
	}
}

func TestThrottleIdentityTrustsForwardedIPOnlyForPermissionedProxyMode(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	request.RemoteAddr = "127.0.0.1:43210"
	request.Header.Set("X-Forwarded-For", "198.51.100.20")

	if got := (&App{}).clientThrottleKey(request); got != opaqueThrottleKey("127.0.0.1") {
		t.Fatalf("direct throttle key trusted spoofed forwarding data: %q", got)
	}
	if got := (&App{trustedProxy: true}).clientThrottleKey(request); got != opaqueThrottleKey("198.51.100.20") {
		t.Fatalf("proxy throttle key = %q", got)
	}
	request.Header.Set("X-Forwarded-For", "198.51.100.20, 203.0.113.8")
	if got := (&App{trustedProxy: true}).clientThrottleKey(request); got != "shared-proxy" {
		t.Fatalf("multi-hop forwarded value = %q, want shared proxy key", got)
	}
}

type browserSession struct{ session, csrf *http.Cookie }

type fakeMailer struct{ messages []providers.Message }

func (m *fakeMailer) Send(_ context.Context, message providers.Message) error {
	m.messages = append(m.messages, message)
	return nil
}

func (m *fakeMailer) last(t *testing.T) providers.Message {
	t.Helper()
	if len(m.messages) == 0 {
		t.Fatal("expected a message")
	}
	return m.messages[len(m.messages)-1]
}

func newAuthTestApp(t *testing.T, allowed ...string) (*App, *fakeMailer) {
	t.Helper()
	application := newTestApp(t)
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	next := 0
	application.auth = auth.New(application.db, auth.Config{
		Now: func() time.Time { return now },
		Token: func() (string, error) {
			next++
			return fmt.Sprintf("test-token-%03d", next), nil
		},
		PasswordSlots: 1,
	})
	application.mailer = &fakeMailer{}
	application.origin, _ = canonicalOrigin(testOrigin, true)
	application.secure = true
	if err := application.auth.SynchronizeAllowlist(context.Background(), allowed); err != nil {
		t.Fatal(err)
	}
	return application, application.mailer.(*fakeMailer)
}

func activate(t *testing.T, application *App, mailer *fakeMailer, email, password string, invitation *http.Cookie) browserSession {
	t.Helper()
	forgot := serve(application, authForm(http.MethodPost, "/auth/forgot-password", url.Values{"email": {email}}, nil))
	if forgot.Code != http.StatusOK {
		t.Fatalf("forgot %s = %d", email, forgot.Code)
	}
	reset := tokenFromMessage(t, mailer.last(t), "token")
	bootstrap := serve(application, httptest.NewRequest(http.MethodGet, "/auth/reset?token="+url.QueryEscape(reset), nil))
	resetCookie := responseCookie(t, bootstrap, application.cookieName(resetCookieName))
	cookies := []*http.Cookie{resetCookie}
	if invitation != nil {
		cookies = append(cookies, invitation)
	}
	setup := serve(application, authForm(http.MethodPost, "/auth/password", url.Values{"password": {password}}, cookies))
	if setup.Code != http.StatusSeeOther {
		t.Fatalf("activate %s = %d %q", email, setup.Code, setup.Body.String())
	}
	return browserSession{session: responseCookie(t, setup, application.cookieName(sessionCookieName)), csrf: responseCookie(t, setup, application.cookieName(csrfCookieName))}
}

func bootstrapInvitation(t *testing.T, application *App, token string) *http.Cookie {
	t.Helper()
	response := serve(application, httptest.NewRequest(http.MethodGet, "/auth/invitation?token="+url.QueryEscape(token), nil))
	if response.Code != http.StatusSeeOther || response.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("invitation bootstrap = %d referrer=%q", response.Code, response.Header().Get("Referrer-Policy"))
	}
	return responseCookie(t, response, application.cookieName(invitationCookieName))
}

func activateInvitation(t *testing.T, application *App, password string, invitation *http.Cookie) browserSession {
	t.Helper()
	setup := serve(application, authForm(http.MethodPost, "/auth/password", url.Values{"password": {password}}, []*http.Cookie{invitation}))
	if setup.Code != http.StatusSeeOther || setup.Header().Get("Location") != "/" {
		t.Fatalf("invitation password setup = %d %q", setup.Code, setup.Body.String())
	}
	return browserSession{session: responseCookie(t, setup, application.cookieName(sessionCookieName)), csrf: responseCookie(t, setup, application.cookieName(csrfCookieName))}
}

func settingsPost(session browserSession, email string) *http.Request {
	return authForm(http.MethodPost, "/settings", url.Values{"email": {email}, "csrf": {session.csrf.Value}}, []*http.Cookie{session.session, session.csrf})
}

func authForm(method, path string, values url.Values, cookies []*http.Cookie) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", testOrigin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.RemoteAddr = "192.0.2.24:9000"
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	return request
}

func serve(application *App, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, request)
	return response
}

func responseCookie(t *testing.T, response *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == name && cookie.MaxAge >= 0 {
			return cookie
		}
	}
	t.Fatalf("missing response cookie %q: %v", name, response.Header().Values("Set-Cookie"))
	return nil
}

func hasClearedCookie(response *httptest.ResponseRecorder, name string) bool {
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == name && cookie.MaxAge < 0 {
			return true
		}
	}
	return false
}

func assertHostCookie(t *testing.T, cookie *http.Cookie, sameSite http.SameSite) {
	t.Helper()
	if !strings.HasPrefix(cookie.Name, "__Host-") || !cookie.HttpOnly || !cookie.Secure || cookie.Path != "/" || cookie.SameSite != sameSite {
		t.Fatalf("unsafe cookie %#v", cookie)
	}
}

func tokenFromMessage(t *testing.T, message providers.Message, key string) string {
	t.Helper()
	parsed, err := url.Parse(lastLine(message.Text))
	if err != nil || parsed.Query().Get(key) == "" {
		t.Fatalf("message has no %s token: %q", key, message.Text)
	}
	return parsed.Query().Get(key)
}

func lastLine(text string) string {
	parts := strings.Split(strings.TrimSpace(text), "\n")
	for index := len(parts) - 1; index >= 0; index-- {
		if strings.HasPrefix(parts[index], "http") {
			return parts[index]
		}
	}
	return ""
}
