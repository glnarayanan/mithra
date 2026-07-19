package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/imports"
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

func TestPublicCLIHelpUsageAndCompletionsDoNotDiscoverHost(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := execute(context.Background(), []string{"help", "install"}, &stdout, &stderr); got != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "Usage: mithra-installer install") {
		t.Fatalf("help exit=%d stdout=%q stderr=%q", got, stdout.String(), stderr.String())
	}
	stdout.Reset()
	if got := execute(context.Background(), []string{"install", "--unknown"}, &stdout, &stderr); got != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("unknown flag exit=%d stdout=%q stderr=%q", got, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if got := execute(context.Background(), []string{"completion", "bash"}, &stdout, &stderr); got != 0 || !strings.Contains(stdout.String(), "complete -F _mithra_installer") || strings.Contains(stdout.String(), "--artifact") {
		t.Fatalf("completion exit=%d stdout=%q stderr=%q", got, stdout.String(), stderr.String())
	}
	stdout.Reset()
	if err := completion("zsh", &stdout); err != nil || !strings.Contains(stdout.String(), "--domain=[option]:value:") || !strings.Contains(stdout.String(), "backup restore") {
		t.Fatalf("zsh completion=%q err=%v", stdout.String(), err)
	}
	stdout.Reset()
	if err := completion("fish", &stdout); err != nil || !strings.Contains(stdout.String(), "-l domain -r") || strings.Contains(stdout.String(), "-l artifact") {
		t.Fatalf("fish completion=%q err=%v", stdout.String(), err)
	}
	stdout.Reset()
	stderr.Reset()
	if got := execute(context.Background(), []string{"install", "--help"}, &stdout, &stderr); got != 0 || !strings.Contains(stdout.String(), "Usage: mithra-installer install") || stderr.Len() != 0 {
		t.Fatalf("command help exit=%d stdout=%q stderr=%q", got, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if got := execute(context.Background(), []string{"backup", "--root", t.TempDir()}, &stdout, &stderr); got != 1 || stdout.Len() != 0 || strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("operational failure exit=%d stdout=%q stderr=%q", got, stdout.String(), stderr.String())
	}
}

func TestEveryPublicCommandHasStdoutHelpAndVersionRejectsArguments(t *testing.T) {
	commands := []string{"help", "install", "upgrade", "reconfigure", "backup", "restore", "status", "reset-demo", "uninstall", "purge", "plan", "verify-backup", "completion", "version"}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := execute(context.Background(), []string{command, "--help"}, &stdout, &stderr); got != 0 || stdout.Len() == 0 || stderr.Len() != 0 {
				t.Fatalf("help exit=%d stdout=%q stderr=%q", got, stdout.String(), stderr.String())
			}
		})
	}
	var stdout, stderr bytes.Buffer
	if got := execute(context.Background(), []string{"version", "extra"}, &stdout, &stderr); got != 2 || stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf("version misuse exit=%d stdout=%q stderr=%q", got, stdout.String(), stderr.String())
	}
}

func TestPlanOperationAndRootValidationAreScoped(t *testing.T) {
	root := t.TempDir()
	parsed, err := parseInstallerCommand([]string{"plan", "uninstall", "--root", root})
	if err != nil || !parsed.planOnly || parsed.operation != installer.Uninstall {
		t.Fatalf("plan parsed=%+v err=%v", parsed, err)
	}
	if _, err := parseInstallerCommand([]string{"backup", "--archive", "wrong"}); err == nil {
		t.Fatal("unrelated flag was accepted")
	}
	if _, err := parseInstallerCommand([]string{"backup", "--root", root + "/../" + filepath.Base(root)}); err == nil {
		t.Fatal("unclean root was accepted")
	}
}

func TestVerifyBackupIsReadOnlyAndDoesNotRevealKey(t *testing.T) {
	root := t.TempDir()
	paths := installer.OwnedPaths(root, installer.AppOnly)
	if err := os.MkdirAll(filepath.Dir(paths.MasterKey), 0o700); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{7}, 32)
	if err := os.WriteFile(paths.MasterKey, []byte(base64.RawURLEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.Sources, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := imports.NewDeletionJournal(paths.Journal, key); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(context.Background(), paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	archive, err := installer.CreateBackup(context.Background(), paths, key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	journalBefore, err := os.Stat(paths.Journal)
	if err != nil {
		t.Fatal(err)
	}
	facts := installer.RuntimeFacts(root, 18090)
	preflightPlanFacts(context.Background(), installer.Restore, paths, archive, &facts)
	if !facts.ArchiveValid || !facts.JournalValid || !facts.KeyMatches {
		t.Fatalf("restore plan facts=%+v", facts)
	}
	journalAfter, err := os.Stat(paths.Journal)
	if err != nil || !journalAfter.ModTime().Equal(journalBefore.ModTime()) {
		t.Fatalf("plan preflight changed journal: %v", err)
	}
	var output bytes.Buffer
	if err := runVerifyBackup([]string{"--root", root, "--archive", archive}, &output); err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Archive  string `json:"archive"`
		Verified bool   `json:"verified"`
	}
	if err := json.Unmarshal(output.Bytes(), &receipt); err != nil || !receipt.Verified || receipt.Archive != filepath.Base(archive) || strings.Contains(output.String(), base64.RawURLEncoding.EncodeToString(key)) {
		t.Fatalf("receipt=%q decoded=%+v err=%v", output.String(), receipt, err)
	}
	after, err := os.Stat(archive)
	if err != nil || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("archive changed after verification: %v", err)
	}
}
