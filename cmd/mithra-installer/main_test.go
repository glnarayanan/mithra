package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/demo"
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

func TestResetDemoRestoresBackupWhenRestartedApplicationIsUnhealthy(t *testing.T) {
	candidateHealth := errors.New("candidate is unhealthy")
	stops, starts, restores := 0, 0, 0
	dependencies := demoResetDependencies{
		reset: func(context.Context, demo.Config) (demo.Receipt, error) {
			return demo.Receipt{BackupArchive: "pre-reset.tar.gz"}, nil
		},
		quiesce: func(context.Context, string) (func() error, error) {
			stops++
			return func() error { starts++; return nil }, nil
		},
		ownership: func(string) (installer.RestoreOwnership, error) {
			return installer.RestoreOwnership{UID: 1001, GID: 1001, Set: true}, nil
		},
		restore: func(_ installer.Paths, archive string, key []byte, owner installer.RestoreOwnership, health func() error) error {
			restores++
			if archive != "pre-reset.tar.gz" || len(key) != 32 || !owner.Set {
				t.Fatalf("rollback inputs archive=%q key=%d owner=%+v", archive, len(key), owner)
			}
			return health()
		},
		wait: func(ctx context.Context, check func(context.Context) error) error { return check(ctx) },
		local: func(context.Context, installer.Plan) error {
			if restores == 0 {
				return candidateHealth
			}
			return nil
		},
		web: func(context.Context, string) error { return nil },
	}
	_, err := resetDemo(context.Background(), installer.Plan{Options: installer.Options{Root: "/", Port: 8090}, Proxy: installer.AppOnly}, installer.Paths{Data: "/var/lib/mithra"}, bytes.Repeat([]byte{1}, 32), demo.Config{}, dependencies)
	if !errors.Is(err, candidateHealth) {
		t.Fatalf("reset demo error=%v", err)
	}
	if stops != 2 || starts != 2 || restores != 1 {
		t.Fatalf("stops=%d starts=%d restores=%d", stops, starts, restores)
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

func TestDemoPasswordFilesArePrivateAndBounded(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owner-password")
	if err := os.WriteFile(path, []byte("owner demo password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("platform does not expose file ownership")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		t.Fatalf("fixture owner uid=%d, effective uid=%d", stat.Uid, os.Geteuid())
	}
	password, err := readDemoPasswordFile(path)
	if err != nil || string(password) != "owner demo password" {
		t.Fatalf("password=%q err=%v", password, err)
	}
	clear(password)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readDemoPasswordFile(path); err == nil {
		t.Fatal("world-readable demo password file was accepted")
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

func TestLegacyInstallMatchesExactlyOneSignedRunningInstaller(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	running := []byte("running-installer")
	digest := sha256.Sum256(running)
	manifest, err := installer.CanonicalManifest(installer.ReleaseManifest{Version: "v1.2.3", Artifacts: map[string]installer.ReleaseArtifact{
		"mithra-installer-linux-amd64": {SHA256: hex.EncodeToString(digest[:]), Size: int64(len(running))},
	}})
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, manifest)
	name, err := signedInstallerArtifact(manifest, signature, publicKey, running)
	if err != nil || name != "mithra-installer-linux-amd64" {
		t.Fatalf("legacy installer match name=%q err=%v", name, err)
	}
	if !isReleaseBuild("v1.2.3") || isReleaseBuild("dev") {
		t.Fatal("release build detection is not strict")
	}
	duplicate, err := installer.CanonicalManifest(installer.ReleaseManifest{Version: "v1.2.3", Artifacts: map[string]installer.ReleaseArtifact{
		"mithra-installer-linux-amd64": {SHA256: hex.EncodeToString(digest[:]), Size: int64(len(running))},
		"mithra-installer-linux-arm64": {SHA256: hex.EncodeToString(digest[:]), Size: int64(len(running))},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signedInstallerArtifact(duplicate, ed25519.Sign(privateKey, duplicate), publicKey, running); err == nil {
		t.Fatal("ambiguous running installer match was accepted")
	}
}

func TestLegacyInstallInvocationAuthenticatesAndInstallsRunningInstaller(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	running, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	appPath := filepath.Join(root, "mithra-linux-amd64")
	app := []byte("application-binary")
	if err := os.WriteFile(appPath, app, 0o600); err != nil {
		t.Fatal(err)
	}
	appDigest, installerDigest := sha256.Sum256(app), sha256.Sum256(running)
	manifest, err := installer.CanonicalManifest(installer.ReleaseManifest{Version: "v1.2.3", Artifacts: map[string]installer.ReleaseArtifact{
		filepath.Base(appPath):                 {SHA256: hex.EncodeToString(appDigest[:]), Size: int64(len(app))},
		"mithra-installer-test-running-binary": {SHA256: hex.EncodeToString(installerDigest[:]), Size: int64(len(running))},
	}})
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "RELEASE-MANIFEST")
	signaturePath := filepath.Join(root, "RELEASE-MANIFEST.sig")
	plunkPath := filepath.Join(root, "plunk.key")
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(signaturePath, ed25519.Sign(privateKey, manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plunkPath, []byte("sk_plunk-test"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldKey, oldVersion := publisherKeyBase64, buildVersion
	publisherKeyBase64 = base64.RawStdEncoding.EncodeToString(publicKey)
	buildVersion = "dev"
	t.Cleanup(func() {
		publisherKeyBase64, buildVersion = oldKey, oldVersion
	})
	plan, err := installer.BuildPlan(installer.Options{Operation: installer.Install, Root: root, Proxy: installer.AppOnly, Port: 18090, AllowedEmails: []string{"owner@example.com"}, PlunkFrom: "Mithra <mail@example.com>"}, installer.HostFacts{OS: "linux", Arch: "amd64", Systemd: true, SQLite: true, Commands: map[string]string{}, AppPortAvailable: true, FreeBytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	paths := installer.OwnedPaths(root, installer.AppOnly)
	if err := os.MkdirAll(paths.Sources, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(context.Background(), paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Journal, []byte("test journal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := installRelease(context.Background(), plan, appPath, "", manifestPath, signaturePath, "", plunkPath); err != nil {
		t.Fatal(err)
	}
	installed, err := os.ReadFile(paths.Installer)
	if err != nil || sha256.Sum256(installed) != installerDigest {
		t.Fatalf("installed running installer digest mismatch err=%v", err)
	}
	if version, err := os.ReadFile(paths.Version); err != nil || string(version) != "v1.2.3\n" {
		t.Fatalf("installed version=%q err=%v", version, err)
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
	parsed, err = parseInstallerCommand([]string{"reset-demo", "--root", root, "--owner-email", "judge-owner@example.com", "--partner-email", "judge-partner@example.com", "--owner-password-file", "/root/owner-password", "--partner-password-file", "/root/partner-password"})
	if err != nil || *parsed.flags.ownerPasswordPath != "/root/owner-password" || *parsed.flags.partnerPasswordPath != "/root/partner-password" {
		t.Fatalf("reset-demo password files parsed=%+v err=%v", parsed, err)
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
