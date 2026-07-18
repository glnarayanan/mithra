package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

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
