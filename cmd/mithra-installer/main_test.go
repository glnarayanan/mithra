package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/glnarayanan/mithra/internal/installer"
)

func TestWaitForHealthRetriesUntilReady(t *testing.T) {
	attempts := 0
	err := waitForHealth(context.Background(), func(context.Context) error {
		attempts++
		if attempts == 1 {
			return errors.New("not ready")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("waitForHealth() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("waitForHealth() attempts = %d, want 2", attempts)
	}
}

func TestCLIRejectsMissingOperationAndNormalizesAllowlist(t *testing.T) {
	if err := run(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "operation required") {
		t.Fatalf("missing operation = %v", err)
	}
	want := []string{"owner@example.com", "partner@example.com"}
	if got := splitEmails(" Owner@example.com,partner@example.com,owner@example.com "); !reflect.DeepEqual(got, want) {
		t.Fatalf("allowlist = %#v", got)
	}
}

func TestReadBoundedRejectsSymlinkAndListenerDoesNotExposeSecrets(t *testing.T) {
	root := t.TempDir()
	credential := filepath.Join(root, "credential")
	if err := os.WriteFile(credential, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(credential, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readBounded(link, 1024); err == nil {
		t.Fatal("credential symlink was accepted")
	}
	if got := listener("caddy", 8090); got != "/run/mithra/mithra.sock" {
		t.Fatalf("proxy listener = %q", got)
	}
}

func TestInstalledAllowlistFencesDemoAccounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mithra.env")
	if err := os.WriteFile(path, []byte("ALLOWED_EMAILS=owner@example.com,partner@example.com\nMITHRA_DB=/var/lib/mithra/mithra.sqlite3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	allowed := installedAllowlist(path)
	if !emailAllowed(allowed, "OWNER@example.com") || !emailAllowed(allowed, "partner@example.com") || emailAllowed(allowed, "unknown@example.com") {
		t.Fatalf("installed allowlist = %#v", allowed)
	}
}

func TestInstalledRuntimeUsesPersistedProxyModeAndAppOrigin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mithra.env")
	if err := os.WriteFile(path, []byte("MITHRA_PROXY_MODE=app-only\nMITHRA_ADDR=127.0.0.1:18091\nMITHRA_CANONICAL_ORIGIN=http://127.0.0.1:18091\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	proxy, domain, port := installedRuntime(path)
	if proxy != "app-only" || domain != "127.0.0.1:18091" || port != 18091 {
		t.Fatalf("persisted runtime proxy=%q domain=%q port=%d", proxy, domain, port)
	}
}

func TestSystemUserExistsTreatsMissingParserIdentityAsAbsent(t *testing.T) {
	exists, err := systemUserExists(context.Background(), "mithra-pdf-test-identity-that-does-not-exist")
	if err != nil || exists {
		t.Fatalf("missing parser identity exists=%t err=%v", exists, err)
	}
}

func TestParserActivationRequiresBothInstalledUnits(t *testing.T) {
	paths := installer.OwnedPaths(t.TempDir(), installer.AppOnly)
	for _, path := range []string{paths.PDFParserService, paths.PDFParserSocket} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("unit"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !parserUnitsPresent(paths) {
		t.Fatal("complete parser units were not recognized")
	}
	if err := os.Remove(paths.PDFParserSocket); err != nil {
		t.Fatal(err)
	}
	if parserUnitsPresent(paths) {
		t.Fatal("partial parser units enabled parser activation")
	}
}
