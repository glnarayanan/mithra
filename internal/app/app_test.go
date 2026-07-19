package app

import (
	"bytes"
	"context"
	"encoding/json"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestHealthReportsReadyAfterDatabaseInitialization(t *testing.T) {
	t.Parallel()

	application := newTestApp(t)
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	application.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("health status = %d, want %d", response.Code, http.StatusOK)
	}
	var payload map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if payload["status"] != "ready" {
		t.Fatalf("health payload = %#v, want ready", payload)
	}
	if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("health Cache-Control = %q, want no-store", cacheControl)
	}
}

func TestHealthRecoversAfterTransientDatabaseContention(t *testing.T) {
	t.Parallel()

	application := newTestApp(t)
	connection, err := application.db.Conn(context.Background())
	if err != nil {
		t.Fatalf("reserve database connection: %v", err)
	}

	blocked := httptest.NewRecorder()
	application.Handler().ServeHTTP(blocked, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if blocked.Code != http.StatusServiceUnavailable {
		_ = connection.Close()
		t.Fatalf("contended health status = %d, want %d", blocked.Code, http.StatusServiceUnavailable)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("release database connection: %v", err)
	}

	recovered := httptest.NewRecorder()
	application.Handler().ServeHTTP(recovered, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recovered.Code != http.StatusOK {
		t.Fatalf("recovered health status = %d, want %d", recovered.Code, http.StatusOK)
	}
}

func TestHealthRemainsUnavailableAfterClose(t *testing.T) {
	t.Parallel()

	application := newTestApp(t)
	if err := application.Close(); err != nil {
		t.Fatalf("close app: %v", err)
	}

	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed health status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

func TestHTTPGuardsBoundRequestsAndRejectUnsupportedMethods(t *testing.T) {
	t.Parallel()

	application := newTestApp(t)
	oversized := bytes.Repeat([]byte("x"), maxRequestBodyBytes+1)
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(oversized))
	response := httptest.NewRecorder()

	application.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
	if got := response.Body.String(); strings.Contains(got, "/Users/") || strings.Contains(got, "panic") {
		t.Fatalf("oversized response leaked internals: %q", got)
	}
	if response.Header().Get("Content-Security-Policy") == "" || response.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("security headers = %#v, want CSP and frame protection", response.Header())
	}
	if got := response.Header().Get("Referrer-Policy"); got != "same-origin" {
		t.Fatalf("Referrer-Policy = %q, want same-origin for browser CSRF evidence", got)
	}

	unsupported := httptest.NewRequest(http.MethodDelete, "/healthz", nil)
	unsupportedResponse := httptest.NewRecorder()
	application.Handler().ServeHTTP(unsupportedResponse, unsupported)
	if unsupportedResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unsupported method status = %d, want %d", unsupportedResponse.Code, http.StatusMethodNotAllowed)
	}
	if allow := unsupportedResponse.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Fatalf("Allow header = %q, want GET, HEAD", allow)
	}
}

func TestHTTPGuardsRecoverPanicsAndIssueRequestIDs(t *testing.T) {
	t.Parallel()

	handler := withHTTPGuards(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("/private/runtime/secret")
	}), log.New(io.Discard, "", 0))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("panic status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if got := response.Body.String(); strings.Contains(got, "/private/") || strings.Contains(got, "panic") {
		t.Fatalf("panic response leaked internals: %q", got)
	}
	if id := response.Header().Get("X-Request-ID"); !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(id) {
		t.Fatalf("request ID = %q, want 32 lower-case hex characters", id)
	}
}

func TestHTTPGuardsDiscardPartialResponseWhenHandlerPanics(t *testing.T) {
	t.Parallel()

	handler := withHTTPGuards(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Untrusted-Response", "secret")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("private response secret"))
		panic("private panic secret")
	}), log.New(io.Discard, "", 0))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("panic status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if got := response.Body.String(); got != "{\"error\":\"internal server error\"}\n" {
		t.Fatalf("panic response = %q, want bounded generic error", got)
	}
	if got := response.Header().Get("X-Untrusted-Response"); got != "" {
		t.Fatalf("panic response retained untrusted header %q", got)
	}
}

func TestHTTPGuardsRejectOversizedResponses(t *testing.T) {
	t.Parallel()

	handler := withHTTPGuards(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxResponseBodyBytes+1))
	}), log.New(io.Discard, "", 0))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("oversized response status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if got := response.Body.String(); got != "{\"error\":\"internal server error\"}\n" {
		t.Fatalf("oversized response = %q, want bounded generic error", got)
	}
}

func TestBriefRendersAccessibleNavigationEmptyStateAndEscapesStatus(t *testing.T) {
	t.Parallel()

	application := newTestApp(t)
	response := httptest.NewRecorder()
	malicious := `</script><script>window.pwned = true</script>`

	application.renderTemplate(context.Background(), response, "brief.html", BriefView{Navigation: navigationForPath("/"), Status: malicious, Freshness: "Up to date"})

	if response.Code != http.StatusOK {
		t.Fatalf("shell status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	for _, required := range []string{
		`<a class="skip-link" href="#main-content">Skip to main content</a>`,
		`aria-label="Primary navigation"`,
		`href="/finance"`,
		`href="/health"`,
		`href="/planning"`,
		`href="/assets/favicon.svg"`,
		`aria-live="polite"`,
		`Add your first update`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("shell is missing %q", required)
		}
	}
	if strings.Contains(body, malicious) {
		t.Fatalf("shell rendered unsafe status: %q", body)
	}
	if !strings.Contains(body, `&lt;/script&gt;&lt;script&gt;window.pwned = true&lt;/script&gt;`) {
		t.Fatalf("shell did not render escaped status: %q", body)
	}
}

func TestRenderFailureLogsWithoutPartialTemplateOutput(t *testing.T) {
	t.Parallel()

	application := newTestApp(t)
	var logs bytes.Buffer
	application.logger = log.New(&logs, "", 0)
	application.templates = template.Must(template.New("brief.html").Parse(`partial template secret{{template "missing" .}}`))
	response := httptest.NewRecorder()
	ctx := context.WithValue(context.Background(), requestIDContextKey{}, "test-request-id")

	application.renderTemplate(ctx, response, "brief.html", BriefView{})

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("render failure status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if got := response.Body.String(); got != "{\"error\":\"internal server error\"}\n" {
		t.Fatalf("render failure response = %q, want bounded generic error", got)
	}
	if strings.Contains(response.Body.String(), "partial template secret") {
		t.Fatalf("render failure response retained partial template bytes: %q", response.Body.String())
	}
	if got, want := logs.String(), "request_id=test-request-id error_code=render_failed\n"; got != want {
		t.Fatalf("render failure log = %q, want %q", got, want)
	}
}

func TestEmbeddedFaviconIsServedWithoutBrowserConsoleFallback(t *testing.T) {
	t.Parallel()

	application := newTestApp(t)
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/assets/favicon.svg", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("favicon status = %d, want %d", response.Code, http.StatusOK)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "image/svg+xml" {
		t.Fatalf("favicon Content-Type = %q, want image/svg+xml", contentType)
	}
	if body := response.Body.String(); !strings.Contains(body, "<svg") || !strings.Contains(body, "Mithra") {
		t.Fatalf("favicon body is not the embedded Mithra mark: %q", body)
	}
	fallback := httptest.NewRecorder()
	application.Handler().ServeHTTP(fallback, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))
	if fallback.Code != http.StatusOK || fallback.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("favicon fallback = %d %q", fallback.Code, fallback.Header().Get("Content-Type"))
	}
}

func newTestApp(t testing.TB) *App {
	t.Helper()
	application, err := New(context.Background(), Config{DatabasePath: filepath.Join(t.TempDir(), "mithra.sqlite3"), MasterKey: bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() {
		if err := application.Close(); err != nil {
			t.Errorf("close app: %v", err)
		}
	})
	return application
}
