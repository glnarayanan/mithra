package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/imports"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/storage"
)

func healthyFacts() HostFacts {
	return HostFacts{OS: "linux", Arch: "amd64", Systemd: true, SQLite: true, Commands: map[string]string{"caddy": "/usr/bin/caddy", "nginx": "/usr/sbin/nginx", "apache2ctl": "/usr/sbin/apache2ctl"}, Listeners: map[int]string{}, VHosts: map[string]string{}, AppPortAvailable: true, FreeBytes: 1 << 30, MigrationClean: true, SQLiteClean: true, KeyMatches: true, AllowlistValid: true, CaddyImportsOwnedDir: true}
}

func TestApplyDataOwnershipRestrictsTheLiveMithraTree(t *testing.T) {
	paths := OwnedPaths(t.TempDir(), AppOnly)
	if err := os.MkdirAll(paths.Sources, 0o777); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(paths.Sources, "source.enc")
	if err := os.WriteFile(file, []byte("ciphertext"), 0o666); err != nil {
		t.Fatal(err)
	}
	owner := RestoreOwnership{UID: os.Getuid(), GID: os.Getgid(), Set: true}
	if err := ApplyDataOwnership(paths, owner); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(paths.Sources); err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("source directory mode=%v err=%v", info.Mode(), err)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("source file mode=%v err=%v", info.Mode(), err)
	}
	unsafe := paths
	unsafe.Data = filepath.Join(filepath.Dir(paths.Data), "other")
	if err := ApplyDataOwnership(unsafe, owner); err == nil {
		t.Fatal("ownership accepted a non-Mithra data tree")
	}
}

func TestBuildPlanCoversProxyAndOperationPreconditionsWithoutArivuMutation(t *testing.T) {
	root := t.TempDir()
	facts := healthyFacts()
	facts.ArivuPaths = []string{filepath.Join(root, "etc/arivu"), filepath.Join(root, "var/lib/arivu")}
	for _, mode := range []ProxyMode{AppOnly, Caddy, Nginx, Apache} {
		plan, err := BuildPlan(Options{Operation: Install, Root: root, Domain: "home.example", Proxy: mode, Port: 18090, AllowedEmails: []string{"owner@example.com"}, PlunkFrom: "Mithra <mail@example.com>"}, facts)
		if err != nil {
			t.Fatalf("%s plan: %v", mode, err)
		}
		if mode == AppOnly && plan.Listener != "127.0.0.1:18090" || mode != AppOnly && !strings.HasSuffix(plan.Listener, "/run/mithra/mithra.sock") {
			t.Fatalf("%s listener = %q", mode, plan.Listener)
		}
		for _, path := range plan.Mutations {
			if hasArivuSegment(path) {
				t.Fatalf("Arivu mutation path %q", path)
			}
		}
	}
	facts.VHosts["home.example"] = "/etc/nginx/sites-enabled/other.conf"
	if _, err := BuildPlan(Options{Operation: Install, Root: root, Domain: "home.example", Proxy: Nginx, Port: 18090}, facts); err == nil || !strings.Contains(err.Error(), "already owned") {
		t.Fatalf("domain collision = %v", err)
	}
	facts = healthyFacts()
	facts.AppPortAvailable = false
	if _, err := BuildPlan(Options{Operation: Install, Root: root, Proxy: AppOnly, Port: 18090}, facts); err == nil || !strings.Contains(err.Error(), "occupied") {
		t.Fatalf("occupied app port = %v", err)
	}
	facts = healthyFacts()
	if _, err := BuildPlan(Options{Operation: Upgrade, Root: root, Proxy: AppOnly, Port: 18090}, facts); err == nil {
		t.Fatal("upgrade without recovery evidence succeeded")
	}
	facts.MithraInstalled, facts.DBExists, facts.KeyExists, facts.BackupExists = true, true, true, true
	if _, err := BuildPlan(Options{Operation: Upgrade, Root: root, Proxy: AppOnly, Port: 18090}, facts); err != nil {
		t.Fatalf("verified upgrade plan: %v", err)
	}
	if _, err := BuildPlan(Options{Operation: Purge, Root: root, Proxy: AppOnly}, facts); err == nil || !strings.Contains(err.Error(), "explicit confirmation") {
		t.Fatalf("unconfirmed purge = %v", err)
	}
	if _, err := BuildPlan(Options{Operation: Purge, Root: root, Proxy: AppOnly, ConfirmPurge: true}, facts); err == nil || !strings.Contains(err.Error(), "uninstall") {
		t.Fatalf("live purge = %v", err)
	}
}

func TestDatabasePreflightAcceptsAnExactOlderMigrationPrefix(t *testing.T) {
	ctx := context.Background()
	migrations, err := database.EmbeddedMigrations()
	if err != nil || len(migrations) < 2 {
		t.Fatalf("embedded migrations = %d, err=%v", len(migrations), err)
	}
	path := filepath.Join(t.TempDir(), "mithra.sqlite3")
	db, err := database.OpenWithMigrations(ctx, path, migrations[:len(migrations)-1])
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := DatabasePreflight(ctx, path); err != nil {
		t.Fatalf("prior release database failed upgrade preflight: %v", err)
	}
}

func TestProxyHostnameAndRuntimeConfigurationAreStrictAndStable(t *testing.T) {
	facts := healthyFacts()
	for _, domain := range []string{"home.example:443", "home.example/path", "home.example\nreverse_proxy evil", "{home.example}", "home..example", "-home.example"} {
		if _, err := BuildPlan(Options{Operation: Install, Root: t.TempDir(), Domain: domain, Proxy: Caddy, Port: 18090, AllowedEmails: []string{"owner@example.com"}, PlunkFrom: "Mithra <mail@example.com>"}, facts); err == nil {
			t.Fatalf("unsafe hostname accepted: %q", domain)
		}
	}
	plan, err := BuildPlan(Options{Operation: Install, Root: t.TempDir(), Proxy: AppOnly, Port: 18090, AllowedEmails: []string{"owner@example.com"}, PlunkFrom: "Mithra <mail@example.com>"}, facts)
	if err != nil {
		t.Fatal(err)
	}
	runtime := RuntimeConfig(plan)
	for _, want := range []string{"MITHRA_PROXY_MODE=app-only", "MITHRA_ADDR=127.0.0.1:18090", "MITHRA_CANONICAL_ORIGIN=http://127.0.0.1:18090"} {
		if !strings.Contains(runtime, want) {
			t.Fatalf("runtime config missing %q:\n%s", want, runtime)
		}
	}
}

func TestOwnedProxyInferenceDoesNotInspectOtherProxyFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc", "nginx"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "nginx", "other.conf"), []byte("server {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := InferOwnedProxyMode(root); got != "" {
		t.Fatalf("inferred proxy from foreign file: %q", got)
	}
	path := OwnedPaths(root, Caddy).Proxy
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("home.example {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := InferOwnedProxyMode(root); got != Caddy {
		t.Fatalf("owned proxy = %q", got)
	}
}

func TestReconfigureRetiresOnlyPreviousOwnedProxyFragment(t *testing.T) {
	root := t.TempDir()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	app, candidate := []byte("mithra-binary"), []byte("installer-binary")
	appDigest, installerDigest := sha256.Sum256(app), sha256.Sum256(candidate)
	manifest, err := CanonicalManifest(ReleaseManifest{Version: "v1.0.0", Artifacts: map[string]ReleaseArtifact{
		"mithra-linux-amd64":           {SHA256: hex.EncodeToString(appDigest[:]), Size: int64(len(app))},
		"mithra-installer-linux-amd64": {SHA256: hex.EncodeToString(installerDigest[:]), Size: int64(len(candidate))},
	}})
	if err != nil {
		t.Fatal(err)
	}
	facts := healthyFacts()
	install, err := BuildPlan(Options{Operation: Install, Root: root, Domain: "home.example", Proxy: Caddy, Port: 18090, AllowedEmails: []string{"owner@example.com"}, PlunkFrom: "Mithra <mail@example.com>"}, facts)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallRelease(install, ReleaseInstall{ArtifactName: "mithra-linux-amd64", InstallerName: "mithra-installer-linux-amd64", Artifact: app, Installer: candidate, Manifest: manifest, Signature: ed25519.Sign(privateKey, manifest), PublisherKey: publicKey, PlunkCredential: "sk_plunk-initial"}); err != nil {
		t.Fatal(err)
	}
	facts.MithraInstalled, facts.DBExists, facts.KeyExists, facts.BackupExists = true, true, true, true
	nginx, err := BuildPlan(Options{Operation: Reconfigure, Root: root, Domain: "home.example", Proxy: Nginx, PreviousProxy: Caddy, Port: 18090, AllowedEmails: []string{"owner@example.com"}, PlunkFrom: "Mithra <mail@example.com>"}, facts)
	if err != nil {
		t.Fatal(err)
	}
	caddyPath, nginxPath := OwnedPaths(root, Caddy).Proxy, OwnedPaths(root, Nginx).Proxy
	if !containsPath(nginx.Retired, caddyPath) || !containsPath(nginx.Mutations, nginxPath) {
		t.Fatalf("proxy switch plan retired=%q mutations=%q", nginx.Retired, nginx.Mutations)
	}
	checks := 0
	err = InstallRelease(nginx, ReleaseInstall{PlunkCredential: "sk_plunk-nginx", Validate: func() error {
		checks++
		if checks == 1 {
			return errors.New("candidate proxy health failed")
		}
		return nil
	}})
	if err == nil || checks != 2 || !exists(caddyPath) || exists(nginxPath) {
		t.Fatalf("failed proxy switch err=%v checks=%d caddy=%t nginx=%t", err, checks, exists(caddyPath), exists(nginxPath))
	}
	if err := InstallRelease(nginx, ReleaseInstall{PlunkCredential: "sk_plunk-nginx"}); err != nil {
		t.Fatal(err)
	}
	if exists(caddyPath) || !exists(nginxPath) {
		t.Fatalf("caddy->nginx fragments caddy=%t nginx=%t", exists(caddyPath), exists(nginxPath))
	}
	var owned struct {
		Paths []string `json:"paths"`
	}
	raw, err := os.ReadFile(OwnedPaths(root, Nginx).OwnedManifest)
	if err != nil || json.Unmarshal(raw, &owned) != nil {
		t.Fatalf("nginx owned manifest raw=%q err=%v", raw, err)
	}
	if containsPath(owned.Paths, caddyPath) || !containsPath(owned.Paths, nginxPath) {
		t.Fatalf("caddy->nginx owned manifest = %q", owned.Paths)
	}
	appOnly, err := BuildPlan(Options{Operation: Reconfigure, Root: root, Proxy: AppOnly, PreviousProxy: Nginx, Port: 18090, AllowedEmails: []string{"owner@example.com"}, PlunkFrom: "Mithra <mail@example.com>"}, facts)
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(appOnly.Retired, nginxPath) {
		t.Fatalf("proxied->app-only did not retire nginx: %q", appOnly.Retired)
	}
	if err := InstallRelease(appOnly, ReleaseInstall{PlunkCredential: "sk_plunk-app-only"}); err != nil {
		t.Fatal(err)
	}
	if exists(nginxPath) {
		t.Fatal("proxied->app-only retained the previous Mithra nginx fragment")
	}
	raw, err = os.ReadFile(OwnedPaths(root, AppOnly).OwnedManifest)
	if err != nil || json.Unmarshal(raw, &owned) != nil {
		t.Fatalf("owned manifest raw=%q err=%v", raw, err)
	}
	if containsPath(owned.Paths, nginxPath) || containsPath(owned.Paths, "") {
		t.Fatalf("owned manifest retained non-target path: %q", owned.Paths)
	}
}

func TestPrepareRestoreRejectsMalformedAllowlistBeforeStaging(t *testing.T) {
	paths := OwnedPaths(t.TempDir(), AppOnly)
	if _, err := PrepareRestore(context.Background(), paths, "unreadable.mbackup", bytes.Repeat([]byte{1}, 32), []string{"owner@example.com", "not an email"}, RestoreOwnership{}); err == nil || !strings.Contains(err.Error(), "invalid allowlisted email") {
		t.Fatalf("malformed allowlist result = %v", err)
	}
	if exists(filepath.Dir(paths.Data)) {
		t.Fatal("restore preflight created a staging parent before allowlist validation")
	}
}

func TestParserUnitsAreOwnedAndConstrained(t *testing.T) {
	root := t.TempDir()
	plan, err := BuildPlan(Options{Operation: Install, Root: root, Proxy: AppOnly, Port: 18090, AllowedEmails: []string{"owner@example.com"}, PlunkFrom: "Mithra <mail@example.com>"}, healthyFacts())
	if err != nil {
		t.Fatal(err)
	}
	paths := OwnedPaths(root, AppOnly)
	for _, path := range []string{paths.PDFParserService, paths.PDFParserSocket} {
		if !containsPath(plan.Mutations, path) {
			t.Fatalf("parser unit not owned: %s", path)
		}
	}
	unit := PDFParserServiceUnit()
	for _, want := range []string{"User=mithra-pdf", "PrivateNetwork=true", "RestrictAddressFamilies=AF_UNIX", "ProtectSystem=strict", "InaccessiblePaths=/var/lib/mithra/mithra.sqlite3 /var/lib/mithra/sources /etc/mithra/credentials"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("parser service missing %q", want)
		}
	}
}

func containsPath(paths []string, target string) bool {
	for _, path := range paths {
		if path == target {
			return true
		}
	}
	return false
}

func TestReleaseSignaturePreventsArtifactAndChecksumSubstitution(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifact := []byte("verified-mithra")
	digest := sha256.Sum256(artifact)
	manifest := ReleaseManifest{Version: "v1.0.0", Artifacts: map[string]ReleaseArtifact{"mithra-linux-amd64": {SHA256: hex.EncodeToString(digest[:]), Size: int64(len(artifact))}}}
	raw, err := CanonicalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, raw)
	if _, err := VerifyRelease(raw, signature, publicKey, "mithra-linux-amd64", artifact); err != nil {
		t.Fatal(err)
	}
	replacement := []byte("substitute")
	replacementDigest := sha256.Sum256(replacement)
	changed, _ := CanonicalManifest(ReleaseManifest{Version: "v1.0.0", Artifacts: map[string]ReleaseArtifact{"mithra-linux-amd64": {SHA256: hex.EncodeToString(replacementDigest[:]), Size: int64(len(replacement))}}})
	if _, err := VerifyRelease(changed, signature, publicKey, "mithra-linux-amd64", replacement); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("substituted release = %v", err)
	}
}

func TestReleaseManifestAuthenticatesBootstrapAndVersionTransitionBeforeMutation(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	app, candidate, bootstrap := []byte("mithra"), []byte("installer"), []byte("#!/bin/sh\n")
	artifacts := map[string]ReleaseArtifact{}
	for name, value := range map[string][]byte{"mithra-linux-amd64": app, "mithra-installer-linux-amd64": candidate, "install.sh": bootstrap} {
		digest := sha256.Sum256(value)
		artifacts[name] = ReleaseArtifact{SHA256: hex.EncodeToString(digest[:]), Size: int64(len(value))}
	}
	raw, err := CanonicalManifest(ReleaseManifest{Version: "v1.2.3", Artifacts: artifacts})
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, raw)
	if _, err := VerifyRelease(raw, signature, publicKey, "install.sh", bootstrap); err != nil {
		t.Fatalf("verified bootstrap: %v", err)
	}
	if _, err := VerifyRelease(raw, signature, publicKey, "install.sh", []byte("tampered")); err == nil {
		t.Fatal("tampered bootstrap was accepted")
	}
	manifest, err := VerifyRelease(raw, signature, publicKey, "mithra-linux-amd64", app)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyReleaseVersion(manifest, "v9.9.9", ""); err == nil {
		t.Fatal("tag mismatch was accepted")
	}
	if err := VerifyReleaseVersion(manifest, "v1.2.3", "v1.2.3"); err == nil {
		t.Fatal("same-version upgrade was accepted")
	}
	if err := VerifyReleaseVersion(manifest, "v1.2.3", "v1.3.0"); err == nil {
		t.Fatal("downgrade was accepted")
	}
	if err := VerifyReleaseVersion(manifest, "v1.2.3", "v1.2"); err == nil {
		t.Fatal("malformed installed version was accepted")
	}
	if err := VerifyReleaseVersion(ReleaseManifest{Version: "v1.2.3-rc1"}, "v1.2.3-rc1", ""); err == nil {
		t.Fatal("malformed candidate version was accepted")
	}
	plan, err := BuildPlan(Options{Operation: Install, Root: t.TempDir(), Proxy: AppOnly, Port: 18090, AllowedEmails: []string{"owner@example.com"}, PlunkFrom: "Mithra <mail@example.com>"}, healthyFacts())
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallRelease(plan, ReleaseInstall{ArtifactName: "mithra-linux-amd64", InstallerName: "mithra-installer-linux-amd64", Artifact: app, Installer: candidate, Manifest: raw, Signature: signature, PublisherKey: publicKey, RequestedVersion: "v9.9.9", PlunkCredential: "sk_plunk"}); err == nil {
		t.Fatal("tag mismatch mutated an install plan")
	}
	if exists(OwnedPaths(plan.Options.Root, AppOnly).Binary) {
		t.Fatal("tag mismatch wrote an owned path")
	}
}

func FuzzParseManifest(f *testing.F) {
	digest := strings.Repeat("0", 64)
	f.Add([]byte("mithra-release-v1\nversion v1.0.0\nartifact install.sh 1 " + digest + "\n"))
	f.Add([]byte("not-a-manifest"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = ParseManifest(raw)
	})
}

func FuzzExtractArchiveAuthenticatesBoundedInput(f *testing.F) {
	key := bytes.Repeat([]byte{0x42}, 32)
	root, err := os.MkdirTemp("", "mithra-archive-fuzz-")
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { _ = os.RemoveAll(root) })
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, "sources"), 0o700); err != nil {
		f.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "sources", "fixture.enc"), []byte("ciphertext fixture"), 0o600); err != nil {
		f.Fatal(err)
	}
	valid := filepath.Join(root, "valid.mbackup")
	if err := writeArchive(source, valid, key); err != nil {
		f.Fatal(err)
	}
	fixtureRaw, err := os.ReadFile(valid)
	if err != nil {
		f.Fatal(err)
	}
	tampered := append([]byte(nil), fixtureRaw...)
	tampered[len(tampered)-1] ^= 1
	f.Add(fixtureRaw)
	f.Add(tampered)
	candidate := filepath.Join(root, "candidate.mbackup")
	stage := filepath.Join(root, "stage")
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 128<<10 {
			t.Skip()
		}
		if err := os.WriteFile(candidate, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(stage); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(stage, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := extractArchive(candidate, stage, key); err != nil {
			return
		}
		got, err := os.ReadFile(filepath.Join(stage, "sources", "fixture.enc"))
		if err != nil || string(got) != "ciphertext fixture" {
			t.Fatalf("extracted fixture = %q, %v", got, err)
		}
	})
}

func TestExtractArchiveRejectsTraversalEntries(t *testing.T) {
	testExtractArchiveEntry(t, "../outside", true)
	testExtractArchiveEntry(t, "/absolute", true)
	testExtractArchiveEntry(t, "records/report..final.json", false)
}

func testExtractArchiveEntry(t *testing.T, entryName string, wantUnsafe bool) {
	t.Helper()
	root := t.TempDir()
	plainPath := filepath.Join(root, "archive.tar.gz")
	plain, err := os.OpenFile(plainPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(plain)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: entryName, Typeflag: tar.TypeReg, Mode: 0o600, Size: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write([]byte("test")); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(tarWriter.Close(), gzipWriter.Close(), plain.Close()); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x42}, 32)
	archivePath := filepath.Join(root, "archive.mbackup")
	if err := encryptArchive(plainPath, archivePath, key); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(root, "stage")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	extractErr := extractArchive(archivePath, stage, key)
	if wantUnsafe && (extractErr == nil || !strings.Contains(extractErr.Error(), "unsafe entry")) {
		t.Fatalf("entry %q error=%v, want unsafe-entry rejection", entryName, extractErr)
	}
	if !wantUnsafe && extractErr != nil {
		t.Fatalf("entry %q rejected: %v", entryName, extractErr)
	}
	if _, err := os.Stat(filepath.Join(root, "outside")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("traversal entry escaped extraction root: %v", err)
	}
}

func TestAtomicOwnedApplyRollsBackAndLeavesArivuByteStable(t *testing.T) {
	root := t.TempDir()
	arivu := filepath.Join(root, "etc", "arivu", "arivu.env")
	if err := os.MkdirAll(filepath.Dir(arivu), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(arivu, []byte("ARIVU=stable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(arivu)
	facts := healthyFacts()
	plan, err := BuildPlan(Options{Operation: Install, Root: root, Proxy: AppOnly, Port: 18090, AllowedEmails: []string{"owner@example.com"}, PlunkFrom: "Mithra <mail@example.com>"}, facts)
	if err != nil {
		t.Fatal(err)
	}
	paths := OwnedPaths(root, AppOnly)
	if err := os.MkdirAll(filepath.Dir(paths.Config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Config, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = ApplyOwnedFiles(plan, []OwnedFile{{Path: paths.Config, Mode: 0o600, Content: []byte("new")}}, func() error { return errors.New("health failed") })
	if err == nil {
		t.Fatal("failed validation did not roll back")
	}
	got, _ := os.ReadFile(paths.Config)
	if string(got) != "old" {
		t.Fatalf("rolled back config = %q", got)
	}
	after, _ := os.ReadFile(arivu)
	if string(after) != string(before) {
		t.Fatalf("Arivu changed: %q", after)
	}
}

func TestFailedUpgradeRestoresPreParserGeneration(t *testing.T) {
	root := t.TempDir()
	paths := OwnedPaths(root, AppOnly)
	if err := os.MkdirAll(filepath.Dir(paths.Binary), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Binary, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	plan := Plan{Options: Options{Operation: Upgrade, Root: root}, Proxy: AppOnly, Mutations: []string{paths.Binary, paths.PDFParserService, paths.PDFParserSocket}}
	checks := 0
	err := ApplyOwnedFiles(plan, []OwnedFile{{Path: paths.Binary, Mode: 0o755, Content: []byte("new")}, {Path: paths.PDFParserService, Mode: 0o644, Content: []byte("service")}, {Path: paths.PDFParserSocket, Mode: 0o644, Content: []byte("socket")}}, func() error {
		checks++
		if checks == 1 {
			return errors.New("candidate health failed")
		}
		if exists(paths.PDFParserService) || exists(paths.PDFParserSocket) {
			return errors.New("parser units leaked into restored generation")
		}
		return nil
	})
	if err == nil || checks != 2 {
		t.Fatalf("failed upgrade err=%v recovery checks=%d", err, checks)
	}
}

func TestAtomicOwnedApplyRejectsSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "etc")); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(Options{Operation: Install, Root: root, Proxy: AppOnly, Port: 18090, AllowedEmails: []string{"owner@example.com"}, PlunkFrom: "Mithra <mail@example.com>"}, healthyFacts())
	if err != nil {
		t.Fatal(err)
	}
	paths := OwnedPaths(root, AppOnly)
	if err := ApplyOwnedFiles(plan, []OwnedFile{{Path: paths.Config, Mode: 0o600, Content: []byte("unsafe")}}, nil); err == nil || !strings.Contains(err.Error(), "unsafe parent") {
		t.Fatalf("symlinked parent result = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "mithra", "mithra.env")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside target was touched: %v", err)
	}
}

func TestArivuBaselineDetectsImmutableArtifactChange(t *testing.T) {
	root := t.TempDir()
	path := rooted(root, "/usr/local/bin/arivu")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stable"), 0o700); err != nil {
		t.Fatal(err)
	}
	baseline := CaptureArivuBaseline(context.Background(), root)
	if len(baseline.Artifacts) != 1 {
		t.Fatalf("baseline artifacts = %v", baseline.Artifacts)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArivuBaseline(context.Background(), root, baseline); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed Arivu baseline = %v", err)
	}
}

func TestCaddyRequiresPreexistingOwnedImport(t *testing.T) {
	facts := healthyFacts()
	facts.CaddyImportsOwnedDir = false
	_, err := BuildPlan(Options{Operation: Install, Root: t.TempDir(), Domain: "home.example", Proxy: Caddy, Port: 18090, AllowedEmails: []string{"owner@example.com"}, PlunkFrom: "Mithra <mail@example.com>"}, facts)
	if err == nil || !strings.Contains(err.Error(), "already import") {
		t.Fatalf("Caddy import prerequisite = %v", err)
	}
}

func TestInstallReconfigureUninstallAndConfirmedPurgePreserveExactBoundaries(t *testing.T) {
	root := t.TempDir()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	appBinary, installerBinary := []byte("mithra-binary"), []byte("installer-binary")
	appDigest, installerDigest := sha256.Sum256(appBinary), sha256.Sum256(installerBinary)
	manifest := ReleaseManifest{Version: "v1.0.0", Artifacts: map[string]ReleaseArtifact{
		"mithra-linux-amd64":           {SHA256: hex.EncodeToString(appDigest[:]), Size: int64(len(appBinary))},
		"mithra-installer-linux-amd64": {SHA256: hex.EncodeToString(installerDigest[:]), Size: int64(len(installerBinary))},
	}}
	raw, err := CanonicalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, raw)
	facts := healthyFacts()
	options := Options{Operation: Install, Root: root, Proxy: AppOnly, Port: 18090, AllowedEmails: []string{"owner@example.com"}, PlunkFrom: "Mithra <mail@example.com>"}
	plan, err := BuildPlan(options, facts)
	if err != nil {
		t.Fatal(err)
	}
	release := ReleaseInstall{ArtifactName: "mithra-linux-amd64", InstallerName: "mithra-installer-linux-amd64", Artifact: appBinary, Installer: installerBinary, Manifest: raw, Signature: signature, PublisherKey: publicKey, PlunkCredential: "sk_plunk-first"}
	invalidCredential := release
	invalidCredential.PlunkCredential = "pk_public"
	if err := InstallRelease(plan, invalidCredential); err == nil || !strings.Contains(err.Error(), "Plunk credential") {
		t.Fatalf("public Plunk key accepted: %v", err)
	}
	if err := InstallRelease(plan, release); err != nil {
		t.Fatal(err)
	}
	paths := OwnedPaths(root, AppOnly)
	masterBefore, err := os.ReadFile(paths.MasterKey)
	if err != nil || strings.TrimSpace(string(masterBefore)) == "" {
		t.Fatalf("master key = %q err=%v", masterBefore, err)
	}
	if got, _ := os.ReadFile(paths.Version); string(got) != "v1.0.0\n" {
		t.Fatalf("version = %q", got)
	}
	if !exists(paths.PDFParserService) || !exists(paths.PDFParserSocket) || !InspectStatus(paths, "127.0.0.1:18090", "v1.0.0").PDFParserSocket {
		t.Fatal("parser units were not installed and reported")
	}
	if err := os.MkdirAll(paths.Data, 0o700); err != nil {
		t.Fatal(err)
	}
	dataMarker := filepath.Join(paths.Data, "household")
	if err := os.WriteFile(dataMarker, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	facts.MithraInstalled, facts.DBExists, facts.KeyExists, facts.BackupExists = true, true, true, true
	reconfigure, err := BuildPlan(Options{Operation: Reconfigure, Root: root, Proxy: AppOnly, Port: 18090, AllowedEmails: []string{"owner@example.com"}, PlunkFrom: "Mithra <new@example.com>"}, facts)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallRelease(reconfigure, ReleaseInstall{PlunkCredential: "sk_plunk-rotated"}); err != nil {
		t.Fatal(err)
	}
	masterAfter, _ := os.ReadFile(paths.MasterKey)
	dataAfter, _ := os.ReadFile(dataMarker)
	plunkAfter, _ := os.ReadFile(paths.PlunkKey)
	if string(masterAfter) != string(masterBefore) || string(dataAfter) != "preserve" || string(plunkAfter) != "sk_plunk-rotated" {
		t.Fatalf("reconfigure changed recovery state master=%t data=%q plunk=%q", string(masterAfter) != string(masterBefore), dataAfter, plunkAfter)
	}
	uninstall, err := BuildPlan(Options{Operation: Uninstall, Root: root, Proxy: AppOnly}, facts)
	if err != nil {
		t.Fatal(err)
	}
	if err := RemoveRuntime(uninstall); err != nil {
		t.Fatal(err)
	}
	facts.MithraInstalled = false
	if exists(paths.Binary) || exists(paths.PlunkKey) || exists(paths.PDFParserService) || exists(paths.PDFParserSocket) || !exists(paths.MasterKey) || !exists(dataMarker) {
		t.Fatalf("uninstall boundary binary=%t plunk=%t parser-service=%t parser-socket=%t key=%t data=%t", exists(paths.Binary), exists(paths.PlunkKey), exists(paths.PDFParserService), exists(paths.PDFParserSocket), exists(paths.MasterKey), exists(dataMarker))
	}
	purge, err := BuildPlan(Options{Operation: Purge, Root: root, Proxy: AppOnly, ConfirmPurge: true}, facts)
	if err != nil {
		t.Fatal(err)
	}
	if err := PurgeRecovery(purge); err != nil {
		t.Fatal(err)
	}
	if exists(paths.MasterKey) || exists(paths.Data) || exists(paths.Backups) {
		t.Fatal("confirmed recovery targets remain after purge")
	}
}

func TestBackupRestoreAuthenticatesGenerationReconcilesDeletionAndClearsAccess(t *testing.T) {
	ctx := context.Background()
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	sourceRoot := t.TempDir()
	paths := OwnedPaths(sourceRoot, AppOnly)
	if err := os.MkdirAll(paths.Data, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	stamp := "2026-07-18T00:00:00Z"
	if _, err := db.Exec(`INSERT INTO users(id,email,status,password_hash,created_at,updated_at) VALUES('owner','owner@example.com','active','password-secret',?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO households(id,status,owner_user_id,created_at,updated_at) VALUES('home','active','owner',?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO household_members(household_id,user_id,role,created_at) VALUES('home','owner','owner',?)`, stamp); err != nil {
		t.Fatal(err)
	}
	store, err := storage.New(db, paths.Sources, key)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.Store(ctx, policy.ActorScope{ActorID: "owner", HouseholdID: "home", Role: "owner"}, []byte("must stay deleted"), storage.Metadata{Family: "csv", Version: 1, Visibility: policy.Shared, LocatorKind: "source", LocatorValue: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	preserved, err := store.Store(ctx, policy.ActorScope{ActorID: "owner", HouseholdID: "home", Role: "owner"}, []byte("keep this import"), storage.Metadata{Family: "csv", Version: 1, Visibility: policy.Shared, LocatorKind: "source", LocatorValue: "preserved"})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.Store(ctx, policy.ActorScope{ActorID: "owner", HouseholdID: "home", Role: "owner"}, []byte("discard this review"), storage.Metadata{Family: "csv", Version: 1, Visibility: policy.Shared, LocatorKind: "source", LocatorValue: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id, state string
		source    storage.Source
	}{{strings.Repeat("b", 32), "committed", source}, {strings.Repeat("c", 32), "committed", preserved}, {strings.Repeat("d", 32), "review", pending}} {
		if _, err := db.Exec(`INSERT INTO document_imports(id,household_id,owner_user_id,visibility,source_id,file_name,document_kind,source_digest,state,proposal_json,expected_shared_revision,expected_personal_revision,committed_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.id, "home", "owner", "shared", item.source.ID, item.state+".csv", "csv", item.source.PlaintextDigest, item.state, "", 0, 0, stamp, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	journal, err := imports.NewDeletionJournal(paths.Journal, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO browser_sessions(token_hash,csrf_hash,user_id,expires_at,created_at) VALUES(?,?,?,?,?)`, strings.Repeat("a", 64), strings.Repeat("b", 64), "owner", "2027-01-01T00:00:00Z", stamp); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	archive, err := CreateBackup(ctx, paths, key, time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Append(imports.DeletionIntent{ID: "delete-1", HouseholdID: "home", OwnerID: "owner", SourceID: source.ID, Digest: source.PlaintextDigest, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	archiveRaw, err := os.ReadFile(archive)
	if err != nil || strings.Contains(string(archiveRaw), "SQLite format 3") {
		t.Fatalf("backup is not encrypted err=%v", err)
	}
	targetRoot := t.TempDir()
	target := OwnedPaths(targetRoot, AppOnly)
	if err := os.MkdirAll(target.Data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyRegular(paths.Journal, target.Journal, 0o600); err != nil {
		t.Fatal(err)
	}
	owner := RestoreOwnership{UID: os.Getuid(), GID: os.Getgid(), Set: true}
	prepared, err := PrepareRestore(ctx, target, archive, key, []string{"owner@example.com"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Cleanup()
	if err := os.WriteFile(archive, []byte("replaced-after-preflight"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestorePrepared(ctx, target, prepared, key, []string{"owner@example.com"}, owner, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	restored, err := database.Open(ctx, target.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var password, status string
	if err := restored.QueryRow(`SELECT password_hash,status FROM users WHERE email='owner@example.com'`).Scan(&password, &status); err != nil || password != "" || status != "pending" {
		t.Fatalf("restored access password=%q status=%q err=%v", password, status, err)
	}
	var sessions int
	_ = restored.QueryRow(`SELECT COUNT(*) FROM browser_sessions`).Scan(&sessions)
	var sourceState, deletedImport string
	_ = restored.QueryRow(`SELECT state FROM sources WHERE id=?`, source.ID).Scan(&sourceState)
	_ = restored.QueryRow(`SELECT state FROM document_imports WHERE id=?`, strings.Repeat("b", 32)).Scan(&deletedImport)
	if sessions != 0 || sourceState != "deleted" || deletedImport != "deleted" {
		t.Fatalf("restored sessions=%d source=%q import=%q", sessions, sourceState, deletedImport)
	}
	if _, err := os.Stat(filepath.Join(target.Sources, source.StorageKey+".enc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted ciphertext err=%v", err)
	}
	var committed, pendingState, pendingSourceState, preservedSourceState string
	if err := restored.QueryRow(`SELECT state FROM document_imports WHERE id=?`, strings.Repeat("c", 32)).Scan(&committed); err != nil || committed != "committed" {
		t.Fatalf("committed import state=%q err=%v", committed, err)
	}
	if err := restored.QueryRow(`SELECT state FROM document_imports WHERE id=?`, strings.Repeat("d", 32)).Scan(&pendingState); err != nil || pendingState != "discarded" {
		t.Fatalf("pending import state=%q err=%v", pendingState, err)
	}
	if err := restored.QueryRow(`SELECT state FROM sources WHERE id=?`, pending.ID).Scan(&pendingSourceState); err != nil || pendingSourceState != "deleted" {
		t.Fatalf("pending source state=%q err=%v", pendingSourceState, err)
	}
	if _, err := os.Stat(filepath.Join(target.Sources, pending.StorageKey+".enc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discarded ciphertext err=%v", err)
	}
	if _, err := restored.Exec(`UPDATE users SET status='active' WHERE id='owner'`); err != nil {
		t.Fatal(err)
	}
	restoredStore, err := storage.New(restored, target.Sources, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := restoredStore.Read(ctx, policy.ActorScope{ActorID: "owner", HouseholdID: "home", Role: "owner"}, pending.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("discarded source read err=%v", err)
	}
	if err := restored.QueryRow(`SELECT state FROM sources WHERE id=?`, preserved.ID).Scan(&preservedSourceState); err != nil || preservedSourceState != "live" {
		t.Fatalf("committed source state=%q err=%v", preservedSourceState, err)
	}
	if plaintext, _, err := restoredStore.Read(ctx, policy.ActorScope{ActorID: "owner", HouseholdID: "home", Role: "owner"}, preserved.ID); err != nil || string(plaintext) != "keep this import" {
		t.Fatalf("committed source read=%q err=%v", plaintext, err)
	}
	if info, err := os.Stat(target.Database); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("restored database mode=%v err=%v", info.Mode(), err)
	}
	if info, err := os.Stat(target.Data); err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("restored data mode=%v err=%v", info.Mode(), err)
	}
	truncated := filepath.Join(t.TempDir(), "truncated.mbackup")
	raw, _ := os.ReadFile(archive)
	if err := os.WriteFile(truncated, raw[:len(raw)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(target.Database)
	if err != nil {
		t.Fatal(err)
	}
	if err := PreflightRestore(ctx, target, truncated, key, []string{"owner@example.com"}, owner); err == nil {
		t.Fatal("truncated backup passed restore preflight")
	}
	after, err := os.ReadFile(target.Database)
	if err != nil || string(after) != string(before) {
		t.Fatalf("restore preflight changed active database err=%v", err)
	}
	if err := RestoreBackup(ctx, OwnedPaths(t.TempDir(), AppOnly), truncated, key, []string{"owner@example.com"}, nil); err == nil {
		t.Fatal("truncated backup restored")
	}
}

func TestRestoreHealthFailureKeepsCurrentGeneration(t *testing.T) {
	ctx := context.Background()
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	sourceRoot := t.TempDir()
	paths := OwnedPaths(sourceRoot, AppOnly)
	if err := os.MkdirAll(paths.Sources, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	journal, err := imports.NewDeletionJournal(paths.Journal, key)
	if err != nil || journal == nil {
		t.Fatal(err)
	}
	archive, err := CreateBackup(ctx, paths, key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	targetRoot := t.TempDir()
	target := OwnedPaths(targetRoot, AppOnly)
	if err := os.MkdirAll(target.Data, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(target.Data, "current")
	if err := os.WriteFile(marker, []byte("stable"), 0o600); err != nil {
		t.Fatal(err)
	}
	healthCalls := 0
	err = RestoreBackup(ctx, target, archive, key, []string{"owner@example.com"}, func() error {
		healthCalls++
		if healthCalls == 1 {
			return errors.New("unhealthy")
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "previous generation restored") {
		t.Fatalf("health rollback error = %v", err)
	}
	if got, _ := os.ReadFile(marker); string(got) != "stable" {
		t.Fatalf("current generation = %q", got)
	}
	if info, err := os.Stat(target.Data); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("previous generation mode=%v err=%v", info.Mode(), err)
	}
}

func FuzzProxyHostnameNeverRendersUnvalidatedInput(f *testing.F) {
	for _, seed := range []string{"home.example", "home.example:443", "evil\nreverse_proxy", "{bad}"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, domain string) {
		plan, err := BuildPlan(Options{Operation: Install, Root: t.TempDir(), Domain: domain, Proxy: Caddy, Port: 18090, AllowedEmails: []string{"owner@example.com"}, PlunkFrom: "Mithra <mail@example.com>"}, healthyFacts())
		if err != nil {
			if got := ProxyConfig(Plan{Proxy: Caddy, Options: Options{Domain: domain}}); got != "" {
				t.Fatalf("invalid domain rendered: %q", got)
			}
			return
		}
		if got := ProxyConfig(plan); strings.ContainsAny(got, "\r\n") && strings.Contains(domain, "\n") {
			t.Fatalf("control hostname rendered: %q", got)
		}
	})
}
