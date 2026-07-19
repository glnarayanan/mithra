package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHelpRequiresAuthenticationAndExplainsCoreBoundaries(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	if response := serve(application, httptest.NewRequest(http.MethodGet, "/help", nil)); response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/auth/login" {
		t.Fatalf("unauthenticated help = %d %q", response.Code, response.Header().Get("Location"))
	}

	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	request := httptest.NewRequest(http.MethodGet, "/help", nil)
	request.AddCookie(session.session)
	request.AddCookie(session.csrf)
	response := serve(application, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated help = %d %q", response.Code, response.Body.String())
	}
	for _, required := range []string{
		"Start here", "Only you", "Shared", "Capture", "Import", "Finance", "Health", "Planning", "Family Brief", "Week in Review", "OpenAI boundary", "Visual PDF transfer", "Deleting a source and recovery",
	} {
		if !strings.Contains(response.Body.String(), required) {
			t.Fatalf("help missing %q: %s", required, response.Body.String())
		}
	}
}

func TestAuthenticatedShellsExposeHelpAndHelpNavigationEscapes(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	for _, path := range []string{"/", "/review", "/capture", "/imports", "/finance", "/health", "/planning", "/settings", "/help"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(session.session)
		request.AddCookie(session.csrf)
		response := serve(application, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `href="/help"`) || !strings.Contains(response.Body.String(), `src="/assets/app.js"`) || !strings.Contains(response.Body.String(), `data-app-shell`) {
			t.Fatalf("shell %s = %d, Help link=%t, quick navigation script=%t", path, response.Code, strings.Contains(response.Body.String(), `href="/help"`), strings.Contains(response.Body.String(), `src="/assets/app.js"`))
		}
		for _, label := range []string{"Family Brief", "Week in Review", "Capture", "Import", "Finance", "Health", "Planning", "Settings", "Help"} {
			if !strings.Contains(response.Body.String(), ">"+label+"</a>") {
				t.Fatalf("shell %s missing navigation label %q", path, label)
			}
		}
		for _, contract := range []string{`data-quick-destination`, `data-quick-navigation-mount`, `data-shortcut-help-trigger`, `aria-keyshortcuts="?"`, `action="/auth/logout"`} {
			if !strings.Contains(response.Body.String(), contract) {
				t.Fatalf("shell %s missing %q", path, contract)
			}
		}
		if path == "/help" && !strings.Contains(response.Body.String(), "Ctrl+K or Command+K") {
			t.Fatalf("help does not explain quick navigation: %q", response.Body.String())
		}
	}
	login := serve(application, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	if login.Code != http.StatusOK || strings.Contains(login.Body.String(), `src="/assets/app.js"`) {
		t.Fatalf("authentication shell quick navigation script=%t", strings.Contains(login.Body.String(), `src="/assets/app.js"`))
	}

	response := httptest.NewRecorder()
	malicious := `<script>window.pwned = true</script>`
	application.renderTemplate(context.Background(), response, "help.html", HelpView{Navigation: []NavigationItem{{Path: "/help", Label: malicious}}})
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), malicious) || !strings.Contains(response.Body.String(), `&lt;script&gt;window.pwned = true&lt;/script&gt;`) {
		t.Fatalf("help navigation was not escaped: %d %q", response.Code, response.Body.String())
	}
}
