package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/imports"
)

func TestPlatformCredentialDirectoryAcceptsReadOnlyMountsOnly(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "master_key")
	if err := os.WriteFile(path, []byte("secret-value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentialFile(path); err == nil {
		t.Fatal("world-readable credential accepted outside a platform directory")
	}
	t.Setenv(credentialDirectoryEnv, directory)
	if value, err := readCredentialFile(path); err != nil || value != "secret-value" {
		t.Fatalf("platform credential = %q, %v", value, err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentialFile(path); err == nil {
		t.Fatal("world-writable platform credential accepted")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(directory, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "key"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentialFile(filepath.Join(nested, "key")); err == nil {
		t.Fatal("credential below the platform directory accepted")
	}
	link := filepath.Join(directory, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentialFile(link); err == nil {
		t.Fatal("symlinked platform credential accepted")
	}
	t.Setenv(credentialDirectoryEnv, "relative/secrets")
	if platformCredentialPath(filepath.Join("relative/secrets", "master_key")) {
		t.Fatal("relative platform directory accepted")
	}
}

func TestTrustedProxiesMustBePrivateNetworks(t *testing.T) {
	networks, err := parseTrustedProxies("10.0.0.0/8, 172.16.0.0/12,192.168.0.0/16 fd00::/8")
	if err != nil || len(networks) != 4 {
		t.Fatalf("private networks = %v, %v", networks, err)
	}
	for _, raw := range []string{"", "0.0.0.0/0", "203.0.113.0/24", "10.0.0.0/4", "not-a-cidr", "::/0"} {
		if _, err := parseTrustedProxies(raw); err == nil {
			t.Errorf("parseTrustedProxies(%q) accepted", raw)
		}
	}
}

func TestContainerListenerRequiresAnExplicitTCPAddress(t *testing.T) {
	listener, err := listenContainer("0.0.0.0:0", "")
	if err != nil {
		t.Fatalf("container listener: %v", err)
	}
	_ = listener.Close()
	if _, err := listenContainer("0.0.0.0:0", "/run/mithra/mithra.sock"); err == nil {
		t.Fatal("container mode accepted a Unix socket")
	}
	for _, address := range []string{"localhost:8080", "0.0.0.0", "example.com:8080"} {
		if _, err := listenContainer(address, ""); err == nil {
			t.Errorf("listenContainer(%q) accepted", address)
		}
	}
	// Outside container mode the wildcard address is still refused.
	if err := validateLoopbackAddress("0.0.0.0:8080"); err == nil {
		t.Fatal("non-container mode accepted a wildcard address")
	}
}

func TestContainerModeRefusesUntrustedOrPlainHTTPConfiguration(t *testing.T) {
	directory := t.TempDir()
	keyPath := filepath.Join(directory, "master.key")
	if err := os.WriteFile(keyPath, []byte(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MITHRA_PROXY_MODE", containerProxyMode)
	t.Setenv("MITHRA_ADDR", "127.0.0.1:0")
	t.Setenv("ALLOWED_EMAILS", "owner@example.com")
	t.Setenv("MITHRA_MASTER_KEY_FILE", keyPath)
	plunkPath := filepath.Join(directory, "plunk.key")
	if err := os.WriteFile(plunkPath, []byte("sk_test_container"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MITHRA_PLUNK_KEY_FILE", plunkPath)
	t.Setenv("MITHRA_PLUNK_FROM", "Mithra <hello@mithra.example>")
	t.Setenv("MITHRA_DB", filepath.Join(directory, "data", "mithra.sqlite3"))
	t.Setenv("MITHRA_PDF_PARSER_SOCKET", "local")

	var failure startupError
	t.Setenv("MITHRA_CANONICAL_ORIGIN", "https://mithra.example")
	if err := run(nil); !errors.As(err, &failure) || failure.stage != "trusted_proxies" {
		t.Fatalf("missing trusted proxies = %v", err)
	}
	t.Setenv("MITHRA_TRUSTED_PROXIES", "10.0.0.0/8")
	t.Setenv("MITHRA_CANONICAL_ORIGIN", "http://127.0.0.1:8080")
	if err := run(nil); !errors.As(err, &failure) || failure.stage != "origin" {
		t.Fatalf("plain HTTP origin = %v", err)
	}
	if !database.EncryptionAvailable() {
		t.Setenv("MITHRA_CANONICAL_ORIGIN", "https://mithra.example")
		if err := run(nil); !errors.As(err, &failure) || failure.stage != "database_encryption" {
			t.Fatalf("container mode without SQLCipher = %v", err)
		}
		if _, err := os.Stat(filepath.Join(directory, "data", "mithra.sqlite3")); !os.IsNotExist(err) {
			t.Fatal("refused container start created a database")
		}
	}
}

func TestBackupScheduleRunsDailyAtAFixedUTCTime(t *testing.T) {
	hour, minute, enabled, err := parseBackupTime("")
	if err != nil || !enabled || hour != 3 || minute != 30 {
		t.Fatalf("default schedule = %d:%d %t %v", hour, minute, enabled, err)
	}
	if _, _, enabled, err := parseBackupTime("off"); err != nil || enabled {
		t.Fatalf("off = %t %v", enabled, err)
	}
	for _, raw := range []string{"24:00", "3", "03:60", "x:y"} {
		if _, _, _, err := parseBackupTime(raw); err == nil {
			t.Errorf("parseBackupTime(%q) accepted", raw)
		}
	}
	schedule := backupSchedule{hour: 3, min: 30, directory: t.TempDir()}
	before := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	if next := schedule.nextRun(before); !next.Equal(time.Date(2026, 10, 12, 3, 30, 0, 0, time.UTC)) {
		t.Fatalf("next run before the hour = %s", next)
	}
	if next := schedule.nextRun(time.Date(2026, 10, 12, 3, 30, 0, 0, time.UTC)); !next.Equal(time.Date(2026, 10, 13, 3, 30, 0, 0, time.UTC)) {
		t.Fatalf("next run at the hour = %s", next)
	}
	if !schedule.catchUpDue(time.Now()) {
		t.Fatal("an empty backup directory must catch up")
	}
	recent := filepath.Join(schedule.directory, "mithra-20261012T033000.000000000Z.mbackup")
	if err := os.WriteFile(recent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if schedule.catchUpDue(time.Now()) {
		t.Fatal("a fresh backup must not trigger a catch-up")
	}
	if !schedule.catchUpDue(time.Now().Add(25 * time.Hour)) {
		t.Fatal("a day-old backup must trigger a catch-up")
	}
}

func TestDataLockRefusesASecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mithra.lock")
	first, err := acquireDataLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireDataLock(path); err == nil || !strings.Contains(err.Error(), "stop the app service") {
		t.Fatalf("second lock = %v", err)
	}
	first.Release()
	second, err := acquireDataLock(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	second.Release()
	if unlocked, err := acquireDataLock(""); err != nil || unlocked == nil {
		t.Fatalf("unconfigured lock = %v", err)
	}
}

func TestGenerateMasterKeyPrintsADecodableKey(t *testing.T) {
	var output bytes.Buffer
	if err := runWithOutput([]string{"generate-master-key"}, &output); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeMasterKey(output.String()); err != nil {
		t.Fatalf("generated key does not decode: %v", err)
	}
	var second bytes.Buffer
	_ = runWithOutput([]string{"generate-master-key"}, &second)
	if second.String() == output.String() {
		t.Fatal("generated keys repeat")
	}
}

func TestHealthcheckProbesTheConfiguredPortOnLoopback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	t.Setenv("MITHRA_ADDR", "0.0.0.0:"+port)
	var output bytes.Buffer
	if err := runWithOutput([]string{"healthcheck"}, &output); err != nil || !strings.Contains(output.String(), "ready") {
		t.Fatalf("healthcheck = %q, %v", output.String(), err)
	}
	server.Close()
	if err := runWithOutput([]string{"healthcheck"}, &output); err == nil {
		t.Fatal("healthcheck passed with the server down")
	}
}

func TestSelfListeningParserExtractsAndRecyclesItself(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "mithra-pdf-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	if err := os.Chmod(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(directory, "pdf-parser.sock")
	// A stale socket from a previous process is replaced, not refused.
	stale, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = stale.Close()

	done := make(chan error, 1)
	go func() { done <- runPDFParser([]string{"--listen", socketPath, "--max-lifetime", "2s"}) }()
	waitForSocket(t, socketPath)
	info, err := os.Lstat(socketPath)
	if err != nil || info.Mode().Perm() != 0o660 {
		t.Fatalf("parser socket mode = %v, %v", info.Mode(), err)
	}

	var output bytes.Buffer
	if err := runPDFParserProbe([]string{"--socket", socketPath}, &output); err != nil {
		t.Fatalf("reachability probe: %v", err)
	}
	output.Reset()
	if err := runPDFParserProbe([]string{"--socket", socketPath, "--sample"}, &output); err != nil || !strings.Contains(output.String(), "pdf_parser=ok") {
		t.Fatalf("extraction probe = %q, %v", output.String(), err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("parser exit = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("parser did not exit after its lifetime")
	}
	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Fatal("parser left its socket behind")
	}
	if err := runPDFParserProbe([]string{"--socket", socketPath}, &output); err == nil {
		t.Fatal("probe passed with the parser stopped")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := (imports.SocketPDFParser{Path: socketPath}).Extract(ctx, []byte("%PDF-1.4"), imports.Limits{MaxPages: 1, MaxText: 1}); !errors.Is(err, imports.ErrParserUnavailable) {
		t.Fatalf("stopped parser error = %v, want ErrParserUnavailable", err)
	}
}

func TestSelfListeningParserRefusesAnOpenDirectory(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "mithra-pdf-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runPDFParser([]string{"--listen", filepath.Join(directory, "pdf-parser.sock")}); err == nil {
		t.Fatal("parser listened in an other-accessible directory")
	}
	if err := runPDFParser([]string{"--listen", "relative.sock"}); err == nil {
		t.Fatal("parser listened on a relative path")
	}
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
			if connection, err := net.Dial("unix", path); err == nil {
				_ = connection.Close()
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("parser socket did not appear")
}
