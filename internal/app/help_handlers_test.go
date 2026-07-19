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
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `href="/help"`) {
			t.Fatalf("shell %s = %d, Help link=%t", path, response.Code, strings.Contains(response.Body.String(), `href="/help"`))
		}
	}

	response := httptest.NewRecorder()
	malicious := `<script>window.pwned = true</script>`
	application.renderTemplate(context.Background(), response, "help.html", HelpView{Navigation: []NavigationItem{{Path: "/help", Label: malicious}}})
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), malicious) || !strings.Contains(response.Body.String(), `&lt;script&gt;window.pwned = true&lt;/script&gt;`) {
		t.Fatalf("help navigation was not escaped: %d %q", response.Code, response.Body.String())
	}
}
