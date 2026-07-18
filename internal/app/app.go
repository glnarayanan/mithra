// Package app composes Mithra's database-backed HTTP application.
package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/glnarayanan/mithra/internal/auth"
	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/finance"
	"github.com/glnarayanan/mithra/internal/health"
	"github.com/glnarayanan/mithra/internal/jobs"
	"github.com/glnarayanan/mithra/internal/planning"
	"github.com/glnarayanan/mithra/internal/providers"
	"github.com/glnarayanan/mithra/internal/secrets"
	"github.com/glnarayanan/mithra/internal/storage"
	"github.com/glnarayanan/mithra/web"
)

const (
	maxRequestBodyBytes  = 1 << 20
	maxResponseBodyBytes = 8 << 20
	healthCheckTimeout   = time.Second
)

// Config is the application configuration required before the HTTP server can
// listen. Network listener policy belongs to cmd/mithra.
type Config struct {
	DatabasePath    string
	AllowedEmails   []string
	CanonicalOrigin string
	SecureCookies   bool
	TrustedProxy    bool
	Mailer          providers.Mailer
	MasterKey       []byte
	OpenAIClient    *http.Client // fixed-endpoint transport seam for tests.
	SourceRoot      string
	Auth            *auth.Service // test seam; production constructs the standard service.
}

// App owns the initialized database and the embedded HTML renderer.
type App struct {
	db               *sql.DB
	templates        *template.Template
	logger           *log.Logger
	auth             *auth.Service
	mailer           providers.Mailer
	providerSettings *secrets.SettingsStore
	openAIClient     *http.Client
	sources          *storage.Service
	jobs             *jobs.Service
	finance          *finance.Service
	healthRecords    *health.Service
	planningRecords  *planning.Service
	origin           *url.URL
	secure           bool
	trustedProxy     bool
	closed           atomic.Bool
	closeOnce        sync.Once
	closeErr         error
}

// ShellView is deliberately limited to text and route state. html/template
// escapes all text fields, including future import and model messages.
type ShellView struct {
	Path       string
	Status     string
	Navigation []NavigationItem
}

// NavigationItem is one primary shell destination.
type NavigationItem struct {
	Path    string
	Label   string
	Current bool
}

// New opens a verified SQLite database before making a handler available.
func New(ctx context.Context, cfg Config) (*App, error) {
	if strings.TrimSpace(cfg.DatabasePath) == "" {
		return nil, errors.New("application database path is required")
	}
	origin, err := canonicalOrigin(cfg.CanonicalOrigin, cfg.SecureCookies)
	if err != nil {
		return nil, err
	}
	allowedEmails, err := normalizeAllowlist(cfg.AllowedEmails)
	if err != nil {
		return nil, err
	}

	templates, err := template.ParseFS(web.Files, "templates/shell.html", "templates/auth/*.html", "templates/finance/*.html", "templates/health/*.html", "templates/planning/*.html", "templates/settings/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse embedded templates: %w", err)
	}
	db, err := database.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return nil, fmt.Errorf("initialize database: %w", err)
	}

	service := cfg.Auth
	if service == nil {
		service = auth.New(db, auth.Config{})
	}
	if err := service.SynchronizeAllowlist(ctx, allowedEmails); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("synchronize allowlist: %w", err)
	}
	mailer := cfg.Mailer
	if mailer == nil {
		mailer = unavailableMailer{}
	}
	providerSettings, err := secrets.NewSettingsStore(db, cfg.MasterKey)
	if err != nil {
		_ = db.Close()
		return nil, errors.New("initialize encrypted settings")
	}
	sourceRoot := strings.TrimSpace(cfg.SourceRoot)
	if sourceRoot == "" {
		sourceRoot = filepath.Join(filepath.Dir(cfg.DatabasePath), "sources")
	}
	sourceRoot, err = filepath.Abs(sourceRoot)
	if err != nil {
		_ = db.Close()
		return nil, errors.New("initialize source storage")
	}
	sources, err := storage.New(db, sourceRoot, cfg.MasterKey)
	if err != nil {
		_ = db.Close()
		return nil, errors.New("initialize source storage")
	}
	if err := sources.Reconcile(ctx); err != nil {
		_ = db.Close()
		return nil, errors.New("reconcile source storage")
	}
	return &App{db: db, templates: templates, logger: log.Default(), auth: service, mailer: mailer, providerSettings: providerSettings, openAIClient: cfg.OpenAIClient, sources: sources, jobs: jobs.New(db), finance: finance.New(db), healthRecords: health.New(db), planningRecords: planning.New(db), origin: origin, secure: cfg.SecureCookies, trustedProxy: cfg.TrustedProxy}, nil
}

// Close prevents future readiness responses and closes the owned database.
func (a *App) Close() error {
	a.closeOnce.Do(func() {
		a.closed.Store(true)
		if a.db != nil {
			a.closeErr = a.db.Close()
		}
	})
	return a.closeErr
}

// Handler provides the complete U1 HTTP surface with safety middleware applied
// to every route.
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", a.health)
	mux.HandleFunc("/api/health", a.health)
	mux.HandleFunc("/assets/", a.asset)
	mux.HandleFunc("/favicon.ico", a.favicon)
	mux.HandleFunc("/manifest.webmanifest", a.manifest)
	mux.HandleFunc("/auth/login", a.login)
	mux.HandleFunc("/auth/forgot-password", a.forgotPassword)
	mux.HandleFunc("/auth/reset", a.bootstrapReset)
	mux.HandleFunc("/auth/invitation", a.bootstrapInvitation)
	mux.HandleFunc("/auth/password", a.passwordSetup)
	mux.HandleFunc("/auth/logout", a.logout)
	mux.HandleFunc("/settings", a.settings)
	mux.HandleFunc("/finance", a.financeLens)
	mux.HandleFunc("/health", a.healthLens)
	mux.HandleFunc("/health/correct", a.correctHealthObservation)
	mux.HandleFunc("/planning", a.planningLens)
	mux.HandleFunc("/planning/events/", a.planningICS)
	mux.HandleFunc("/sources/", a.sourceFile)
	mux.HandleFunc("/", a.shell)
	return withHTTPGuards(mux, a.logger)
}

func (a *App) health(w http.ResponseWriter, r *http.Request) {
	if !allowsRead(r.Method) {
		methodNotAllowed(w)
		return
	}
	if a.closed.Load() {
		writeError(w, http.StatusServiceUnavailable, "service is not ready")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), healthCheckTimeout)
	defer cancel()
	if err := a.db.PingContext(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "service is not ready")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (a *App) asset(w http.ResponseWriter, r *http.Request) {
	if !allowsRead(r.Method) {
		methodNotAllowed(w)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/assets/")
	contentType, ok := assetContentTypes[name]
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	a.serveEmbeddedFile(w, r, "static/"+name, contentType)
}

func (a *App) manifest(w http.ResponseWriter, r *http.Request) {
	if !allowsRead(r.Method) {
		methodNotAllowed(w)
		return
	}
	a.serveEmbeddedFile(w, r, "static/manifest.webmanifest", "application/manifest+json; charset=utf-8")
}

func (a *App) favicon(w http.ResponseWriter, r *http.Request) {
	if !allowsRead(r.Method) {
		methodNotAllowed(w)
		return
	}
	a.serveEmbeddedFile(w, r, "static/favicon.svg", "image/svg+xml")
}

func (a *App) serveEmbeddedFile(w http.ResponseWriter, r *http.Request, name, contentType string) {
	data, err := web.Files.ReadFile(name)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

func (a *App) shell(w http.ResponseWriter, r *http.Request) {
	if !allowsRead(r.Method) {
		methodNotAllowed(w)
		return
	}
	if !isShellPath(r.URL.Path) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if _, ok := a.sessionScope(r); !ok {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		return
	}
	a.renderShell(r.Context(), w, ShellView{Path: r.URL.Path})
}

func (a *App) renderShell(ctx context.Context, w http.ResponseWriter, view ShellView) {
	if !isShellPath(view.Path) {
		view.Path = "/"
	}
	if view.Status == "" {
		view.Status = "Mithra is ready for the first household record."
	}
	if view.Navigation == nil {
		view.Navigation = navigationForPath(view.Path)
	}
	rendered := newBufferedResponse(maxResponseBodyBytes)
	if err := a.templates.ExecuteTemplate(rendered, "shell.html", view); err != nil || rendered.overflow {
		logRequestError(a.logger, ctx, "render_failed")
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	rendered.commit(w)
}

func isShellPath(path string) bool {
	switch path {
	case "/", "/finance", "/health", "/planning":
		return true
	default:
		return false
	}
}

func navigationForPath(path string) []NavigationItem {
	return []NavigationItem{
		{Path: "/", Label: "Brief", Current: path == "/"},
		{Path: "/finance", Label: "Finance", Current: path == "/finance"},
		{Path: "/health", Label: "Health", Current: path == "/health"},
		{Path: "/planning", Label: "Planning", Current: path == "/planning"},
		{Path: "/settings", Label: "Settings"},
	}
}

func allowsRead(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func methodNotAllowed(w http.ResponseWriter) {
	methodNotAllowedFor(w, "GET, HEAD")
}

func methodNotAllowedFor(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

var assetContentTypes = map[string]string{
	"styles.css":  "text/css; charset=utf-8",
	"app.js":      "application/javascript; charset=utf-8",
	"finance.js":  "application/javascript; charset=utf-8",
	"health.js":   "application/javascript; charset=utf-8",
	"planning.js": "application/javascript; charset=utf-8",
	"favicon.svg": "image/svg+xml",
}

func withHTTPGuards(next http.Handler, logger *log.Logger) http.Handler {
	return withRequestID(withSecurityHeaders(recoverPanics(limitRequestBody(next), logger)))
}

func limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > maxRequestBodyBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		next.ServeHTTP(w, r)
	})
}

func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'self'; connect-src 'self'; font-src 'self'; form-action 'self'; frame-ancestors 'none'; img-src 'self' data:; manifest-src 'self'; object-src 'none'; script-src 'self'; style-src 'self'")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Origin-Agent-Cluster", "?1")
		w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=(self)")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Permitted-Cross-Domain-Policies", "none")
		next.ServeHTTP(w, r)
	})
}

func recoverPanics(next http.Handler, logger *log.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buffered := newBufferedResponse(maxResponseBodyBytes)
		defer func() {
			if recovered := recover(); recovered != nil {
				logRequestError(logger, r.Context(), "panic_recovered")
				writeError(w, http.StatusInternalServerError, "internal server error")
				return
			}
			if buffered.overflow {
				logRequestError(logger, r.Context(), "response_body_limit_exceeded")
				writeError(w, http.StatusInternalServerError, "internal server error")
				return
			}
			buffered.commit(w)
		}()
		next.ServeHTTP(buffered, r)
	})
}

type bufferedResponse struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	limit    int
	overflow bool
}

func newBufferedResponse(limit int) *bufferedResponse {
	return &bufferedResponse{
		header: make(http.Header),
		limit:  limit,
	}
}

func (w *bufferedResponse) Header() http.Header {
	return w.header
}

func (w *bufferedResponse) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	if status < 100 || status > 999 {
		panic(fmt.Sprintf("invalid WriteHeader code %d", status))
	}
	w.status = status
}

func (w *bufferedResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.overflow || len(data) > w.limit-w.body.Len() {
		w.overflow = true
		return 0, http.ErrContentLength
	}
	return w.body.Write(data)
}

func (w *bufferedResponse) commit(destination http.ResponseWriter) {
	for name, values := range w.header {
		destination.Header().Del(name)
		for _, value := range values {
			destination.Header().Add(name, value)
		}
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	destination.WriteHeader(status)
	_, _ = destination.Write(w.body.Bytes())
}

type requestIDContextKey struct{}

var requestIDFallback atomic.Uint64

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, id)))
	})
}

func newRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	fallback := fmt.Sprintf("%d-%d", time.Now().UnixNano(), requestIDFallback.Add(1))
	digest := sha256.Sum256([]byte(fallback))
	return hex.EncodeToString(digest[:16])
}

func requestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDContextKey{}).(string); ok {
		return id
	}
	return "unknown"
}

func logRequestError(logger *log.Logger, ctx context.Context, errorCode string) {
	logger.Printf("request_id=%s error_code=%s", requestIDFromContext(ctx), errorCode)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
