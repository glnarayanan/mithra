package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/household"
)

func TestValidateLoopbackAddressAcceptsOnlyLiteralLoopbackAddresses(t *testing.T) {
	t.Parallel()

	for _, address := range []string{"127.0.0.1:0", "127.23.45.67:8090", "[::1]:8090"} {
		if err := validateLoopbackAddress(address); err != nil {
			t.Errorf("validateLoopbackAddress(%q) = %v, want success", address, err)
		}
	}

	for _, address := range []string{
		":8090",
		"0.0.0.0:8090",
		"[::]:8090",
		"192.0.2.10:8090",
		"localhost:8090",
		"example.com:8090",
		"127.0.0.1",
	} {
		if err := validateLoopbackAddress(address); err == nil {
			t.Errorf("validateLoopbackAddress(%q) unexpectedly succeeded", address)
		}
	}
}

func TestListenUnixSocketRestrictsPermissionsAndCleansUp(t *testing.T) {
	path := unixSocketTestPath(t)
	listener, err := listenUnixSocket(path)
	if err != nil {
		t.Fatalf("listen on Unix socket: %v", err)
	}

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect Unix socket: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket mode = %s, want Unix socket", info.Mode())
	}
	if permissions := info.Mode().Perm(); permissions != unixSocketMode {
		t.Errorf("socket permissions = %04o, want %04o", permissions, unixSocketMode)
	}

	if err := listener.Close(); err != nil {
		t.Fatalf("close Unix socket listener: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("closed Unix socket still exists: %v", err)
	}
}

func TestListenUnixSocketRejectsCollisionsAndUnsafePaths(t *testing.T) {
	t.Run("non-socket collision", func(t *testing.T) {
		path := unixSocketTestPath(t)
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("create colliding file: %v", err)
		}

		_, err := listenUnixSocket(path)
		if err == nil || !strings.Contains(err.Error(), "not a socket") {
			t.Fatalf("non-socket collision error = %v, want rejection", err)
		}
	})

	t.Run("stale socket collision", func(t *testing.T) {
		path := unixSocketTestPath(t)
		listener, err := listenUnixSocket(path)
		if err != nil {
			t.Fatalf("create Unix socket: %v", err)
		}
		defer listener.Close()

		_, err = listenUnixSocket(path)
		if err == nil || !strings.Contains(err.Error(), "refuse to replace") {
			t.Fatalf("socket collision error = %v, want stale-socket rejection", err)
		}
	})

	t.Run("relative path", func(t *testing.T) {
		_, err := listenUnixSocket("mithra.sock")
		if err == nil || !strings.Contains(err.Error(), "must be absolute") {
			t.Fatalf("relative socket path error = %v, want rejection", err)
		}
	})

	t.Run("other-accessible parent", func(t *testing.T) {
		path := unixSocketTestPath(t)
		directory := filepath.Dir(path)
		if err := os.Chmod(directory, 0o755); err != nil {
			t.Fatalf("make socket directory other-accessible: %v", err)
		}
		_, err := listenUnixSocket(path)
		if err == nil || !strings.Contains(err.Error(), "must deny all permissions to other users") {
			t.Fatalf("other-accessible socket parent error = %v, want rejection", err)
		}
	})

	t.Run("group-writable parent", func(t *testing.T) {
		path := unixSocketTestPath(t)
		directory := filepath.Dir(path)
		if err := os.Chmod(directory, 0o770); err != nil {
			t.Fatalf("make socket directory group-writable: %v", err)
		}
		_, err := listenUnixSocket(path)
		if err == nil || !strings.Contains(err.Error(), "group or world writable") {
			t.Fatalf("group-writable socket parent error = %v, want rejection", err)
		}
	})

	t.Run("world-writable parent", func(t *testing.T) {
		path := unixSocketTestPath(t)
		directory := filepath.Dir(path)
		if err := os.Chmod(directory, 0o702); err != nil {
			t.Fatalf("make socket directory world-writable: %v", err)
		}
		_, err := listenUnixSocket(path)
		if err == nil || !strings.Contains(err.Error(), "group or world writable") {
			t.Fatalf("world-writable socket parent error = %v, want rejection", err)
		}
	})

	t.Run("owner-and-proxy-group parent", func(t *testing.T) {
		path := unixSocketTestPath(t)
		directory := filepath.Dir(path)
		if err := os.Chmod(directory, 0o750); err != nil {
			t.Fatalf("make socket directory proxy-group accessible: %v", err)
		}
		listener, err := listenUnixSocket(path)
		if err != nil {
			t.Fatalf("listen with owner-and-proxy-group socket parent: %v", err)
		}
		if err := listener.Close(); err != nil {
			t.Fatalf("close owner-and-proxy-group Unix socket listener: %v", err)
		}
	})
}

func TestListenRejectsConfiguredLoopbackAndUnixSocketTogether(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mithra.sock")
	if err := run([]string{"--addr", "127.0.0.1:0", "--socket", path}); err == nil || !strings.Contains(err.Error(), "either --addr or --socket") {
		t.Fatalf("explicit listener-mode error = %v, want rejection", err)
	}

	t.Setenv("MITHRA_ADDR", "127.0.0.1:0")
	if err := run([]string{"--socket", path}); err == nil || !strings.Contains(err.Error(), "either --addr or --socket") {
		t.Fatalf("environment listener-mode error = %v, want rejection", err)
	}
}

func TestStartupFailureLoggingRedactsSensitiveCLIAndPathText(t *testing.T) {
	t.Parallel()

	const sensitive = "--not-a-real-option=/private/mithra/household.sqlite3?token=not-for-logs"
	if err := run([]string{sensitive}); err == nil {
		t.Fatal("invalid CLI argument unexpectedly succeeded")
	}

	var logs bytes.Buffer
	logStartupFailure(&logs, failStartup("master_credential", errors.New(sensitive)))
	if got, want := logs.String(), "error_code=startup_failed stage=master_credential\n"; got != want {
		t.Fatalf("startup log = %q, want %q", got, want)
	}
	if strings.Contains(logs.String(), sensitive) || strings.Contains(logs.String(), "/private/mithra") {
		t.Fatalf("startup log leaked sensitive CLI data: %q", logs.String())
	}
	logs.Reset()
	logStartupFailure(&logs, errors.New(sensitive))
	if got, want := logs.String(), "error_code=startup_failed stage=command\n"; got != want {
		t.Fatalf("generic startup log = %q, want %q", got, want)
	}
}

func TestRecoverOwnerCommandUsesCurrentAllowlistAndLeavesPasswordBootstrapPending(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "mithra.sqlite3")
	db, err := database.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	service := household.New(db, household.Config{})
	if err := service.SyncAllowlist(ctx, []string{"former@example.com"}); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`UPDATE users SET status='active',password_hash='retired-hash' WHERE email='former@example.com'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO households(id,status,owner_user_id,created_at,updated_at) SELECT 'closed-home','active',id,?,? FROM users WHERE email='former@example.com'`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO household_members(household_id,user_id,role,created_at) SELECT 'closed-home',id,'owner',? FROM users WHERE email='former@example.com'`, stamp); err != nil {
		t.Fatal(err)
	}
	if err := service.SyncAllowlist(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{"recover-owner", "--db", databasePath, "--allowed-emails", "former@example.com", "--household", "closed-home", "--email", "former@example.com"}); err != nil {
		t.Fatalf("recover-owner command: %v", err)
	}

	db, err = database.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var householdStatus, userStatus string
	if err := db.QueryRow(`SELECT h.status,u.status FROM households h JOIN users u ON u.id=h.owner_user_id WHERE h.id='closed-home'`).Scan(&householdStatus, &userStatus); err != nil {
		t.Fatal(err)
	}
	if householdStatus != "active" || userStatus != "pending" {
		t.Fatalf("recovery state = household %q user %q", householdStatus, userStatus)
	}
}

func TestRuntimeIdentityConfiguration(t *testing.T) {
	emails, err := parseAllowedEmails(" Owner@Example.com,partner@example.com;owner@example.com ")
	if err != nil || len(emails) != 2 || emails[0] != "owner@example.com" || emails[1] != "partner@example.com" {
		t.Fatalf("allowed emails = %#v, %v", emails, err)
	}
	for _, raw := range []string{"", "not-an-email"} {
		if _, err := parseAllowedEmails(raw); err == nil {
			t.Fatalf("parseAllowedEmails(%q) unexpectedly succeeded", raw)
		}
	}
	if secure, err := secureCookiesForOrigin("https://mithra.example"); err != nil || !secure {
		t.Fatalf("HTTPS origin = secure %t, %v", secure, err)
	}
	if secure, err := secureCookiesForOrigin("http://127.0.0.1:8090"); err != nil || secure {
		t.Fatalf("loopback origin = secure %t, %v", secure, err)
	}
	for _, raw := range []string{"http://mithra.example", "https://mithra.example/path", ""} {
		if _, err := secureCookiesForOrigin(raw); err == nil {
			t.Fatalf("secureCookiesForOrigin(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestDecodeMasterKeyRequiresThirtyTwoRandomBytes(t *testing.T) {
	encoded := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	key, err := decodeMasterKey(encoded)
	if err != nil || len(key) != 32 {
		t.Fatalf("decoded key = %d bytes, %v", len(key), err)
	}
	for _, invalid := range []string{"", "not-base64", base64.RawURLEncoding.EncodeToString(make([]byte, 31)), base64.RawURLEncoding.EncodeToString(make([]byte, 33))} {
		if _, err := decodeMasterKey(invalid); err == nil {
			t.Fatalf("invalid master key %q accepted", invalid)
		}
	}
}

func TestReadCredentialFileRequiresPrivateStableRegularFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "plunk")
	if err := os.WriteFile(path, []byte("  sk_test_secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := readCredentialFile(path)
	if err != nil || value != "sk_test_secret" {
		t.Fatalf("read private credential = %q, %v", value, err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentialFile(path); err == nil || strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "sk_test_secret") {
		t.Fatalf("permissive credential error = %v", err)
	}
	link := filepath.Join(directory, "plunk-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentialFile(link); err == nil {
		t.Fatal("symlink credential unexpectedly accepted")
	}
	os.Remove(link)
	t.Setenv("CREDENTIALS_DIRECTORY", directory)
	if value, err := readCredentialFile(path); err != nil || value != "sk_test_secret" {
		t.Fatalf("systemd credential = %q, %v", value, err)
	}
}

func TestUnixSocketCleansUpAfterHTTPServerShutdown(t *testing.T) {
	path := unixSocketTestPath(t)
	listener, err := listenUnixSocket(path)
	if err != nil {
		t.Fatalf("listen on Unix socket: %v", err)
	}

	server := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}
	serveResults := make(chan error, 1)
	go func() { serveResults <- server.Serve(listener) }()

	connection, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatalf("connect to Unix socket: %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("close Unix socket connection: %v", err)
	}

	shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		t.Fatalf("shut down HTTP server: %v", err)
	}
	if err := <-serveResults; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("serve result = %v, want ErrServerClosed", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("shutdown Unix socket still exists: %v", err)
	}
}

func unixSocketTestPath(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "mithra-socket-")
	if err != nil {
		t.Fatalf("create short socket test directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("remove socket test directory: %v", err)
		}
	})
	return filepath.Join(directory, "mithra.sock")
}
