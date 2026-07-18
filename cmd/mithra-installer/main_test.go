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
