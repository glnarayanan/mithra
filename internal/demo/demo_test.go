package demo

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/auth"
	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/imports"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/providers"
	"github.com/glnarayanan/mithra/internal/secrets"
)

func TestPublishedSampleFilesRemainValid(t *testing.T) {
	financeRaw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "demo", "finance.csv"))
	if err != nil || string(financeRaw) != string(sharedFinanceCSV()) {
		t.Fatalf("finance sample drifted err=%v", err)
	}
	planningRaw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "demo", "planning.txt"))
	if err != nil || string(planningRaw) != "We agreed to review our travel documents together on Saturday morning.\n" {
		t.Fatalf("planning sample drifted err=%v", err)
	}
	healthRaw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "demo", "health-report.pdf"))
	if err != nil || string(healthRaw) != string(healthPDF()) {
		t.Fatalf("health sample drifted err=%v", err)
	}
	document, err := imports.New(nil).Extract(context.Background(), imports.Input{Name: "health-report.pdf", ContentType: "application/pdf", Bytes: healthRaw})
	if err != nil || len(document.Fragments) != 1 {
		t.Fatalf("health sample fragments=%d err=%v", len(document.Fragments), err)
	}
}

func TestResetUsesProductionPathsIsFixtureOnlyAndRollsBackExactly(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	cfg := Config{
		DatabasePath: filepath.Join(dataRoot, "mithra.sqlite3"),
		SourceRoot:   filepath.Join(dataRoot, "sources"),
		BackupRoot:   filepath.Join(root, "backups"),
		OwnerEmail:   "judge-owner@example.com",
		PartnerEmail: "judge-partner@example.com",
		MasterKey:    testKey(),
	}
	seedUnrelatedHousehold(t, ctx, cfg.DatabasePath)
	receipt, err := Reset(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.HouseholdID != HouseholdID || receipt.SharedRecords < 1 || receipt.OwnerPersonal < 1 || receipt.PartnerPersonal < 1 {
		t.Fatalf("incomplete receipt: %+v", receipt)
	}
	assertFixtureAndUnrelated(t, ctx, cfg.DatabasePath)
	first := snapshotState(t, ctx, cfg.DatabasePath, cfg.SourceRoot)

	if _, err := Reset(ctx, cfg); err != nil {
		t.Fatalf("repeat reset: %v", err)
	}
	assertFixtureAndUnrelated(t, ctx, cfg.DatabasePath)
	second := snapshotState(t, ctx, cfg.DatabasePath, cfg.SourceRoot)
	if first.tableCounts != second.tableCounts {
		t.Fatalf("reset is not stable: first=%v second=%v", first.tableCounts, second.tableCounts)
	}

	forced := errors.New("forced candidate failure")
	cfg.BeforeComplete = func() error { return forced }
	if _, err := Reset(ctx, cfg); err == nil || !errors.Is(err, forced) {
		t.Fatalf("forced reset failure = %v", err)
	}
	afterFailure := snapshotState(t, ctx, cfg.DatabasePath, cfg.SourceRoot)
	if second.tableCounts != afterFailure.tableCounts || fmt.Sprint(second.sourceDigests) != fmt.Sprint(afterFailure.sourceDigests) {
		t.Fatalf("failed reset changed prior state: before=%v/%v after=%v/%v", second.tableCounts, second.sourceDigests, afterFailure.tableCounts, afterFailure.sourceDigests)
	}
}

func TestResetRefusesUnmarkedReservedHousehold(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "data", "mithra.sqlite3")
	db, err := database.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO users(id,email,status,password_hash,created_at,updated_at) VALUES('ordinary-owner','ordinary@example.com','active','hash',?,?); INSERT INTO households(id,status,owner_user_id,created_at,updated_at) VALUES(?,'active','ordinary-owner',?,?)`, stamp, stamp, HouseholdID, stamp, stamp); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	_, err = Reset(ctx, Config{DatabasePath: databasePath, SourceRoot: filepath.Join(root, "data", "sources"), BackupRoot: filepath.Join(root, "backups"), OwnerEmail: "judge-owner@example.com", PartnerEmail: "judge-partner@example.com", MasterKey: testKey()})
	if !errors.Is(err, ErrUnsafeReset) {
		t.Fatalf("unmarked household reset = %v", err)
	}
	if entries, readErr := os.ReadDir(filepath.Join(root, "backups")); readErr == nil && len(entries) != 0 {
		t.Fatalf("refused reset created backups: %v", entries)
	} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatal(readErr)
	}
	db, err = database.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var owner string
	if err := db.QueryRow(`SELECT owner_user_id FROM households WHERE id=?`, HouseholdID).Scan(&owner); err != nil || owner != "ordinary-owner" {
		t.Fatalf("unmarked household changed owner=%q err=%v", owner, err)
	}
}

func TestResetSetsPrivateJudgeCredentialsAndRevokesBootstrapSessions(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := Config{
		DatabasePath:    filepath.Join(root, "data", "mithra.sqlite3"),
		SourceRoot:      filepath.Join(root, "data", "sources"),
		BackupRoot:      filepath.Join(root, "backups"),
		OwnerEmail:      "judge-owner@example.com",
		PartnerEmail:    "judge-partner@example.com",
		OwnerPassword:   []byte("owner demo password"),
		PartnerPassword: []byte("partner demo password"),
		MasterKey:       testKey(),
	}
	seedUnrelatedHousehold(t, ctx, cfg.DatabasePath)
	if _, err := Reset(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	// Reset clears the credential buffers; reload the known synthetic values to
	// prove a repeated hosted-demo reset remains usable.
	cfg.OwnerPassword = []byte("owner demo password")
	cfg.PartnerPassword = []byte("partner demo password")
	if _, err := Reset(ctx, cfg); err != nil {
		t.Fatalf("repeat reset with credentials: %v", err)
	}
	db, err := database.Open(ctx, cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := auth.New(db, auth.Config{})
	for _, account := range []struct {
		email, password, role string
	}{
		{"judge-owner@example.com", "owner demo password", "owner"},
		{"judge-partner@example.com", "partner demo password", "adult"},
	} {
		session, err := service.Login(ctx, account.email, account.password, "demo-credential-test:"+account.email)
		if err != nil || session.Scope.HouseholdID != HouseholdID || session.Scope.Role != account.role {
			t.Fatalf("login %s scope=%+v err=%v", account.email, session.Scope, err)
		}
		if err := service.RevokeSession(ctx, session.Cookie); err != nil {
			t.Fatal(err)
		}
		var hash string
		if err := db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE email=?`, account.email).Scan(&hash); err != nil || hash == account.password || !strings.HasPrefix(hash, "$argon2id$") {
			t.Fatalf("stored password hash for %s = %q err=%v", account.email, hash, err)
		}
	}
	var activeBootstrapSessions int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM browser_sessions WHERE user_id IN (?,?) AND revoked_at IS NULL`, ownerSeedID, partnerSeedID).Scan(&activeBootstrapSessions); err != nil || activeBootstrapSessions != 0 {
		t.Fatalf("active bootstrap sessions=%d err=%v", activeBootstrapSessions, err)
	}
	assertFixtureAndUnrelated(t, ctx, cfg.DatabasePath)
}

func TestResetPreservesMarkedProviderConfiguration(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	key := testKey()
	cfg := Config{
		DatabasePath:    filepath.Join(root, "data", "mithra.sqlite3"),
		SourceRoot:      filepath.Join(root, "data", "sources"),
		BackupRoot:      filepath.Join(root, "backups"),
		OwnerEmail:      "judge-owner@example.com",
		PartnerEmail:    "judge-partner@example.com",
		OwnerPassword:   []byte("owner demo password"),
		PartnerPassword: []byte("partner demo password"),
		MasterKey:       key,
	}
	seedUnrelatedHousehold(t, ctx, cfg.DatabasePath)
	if _, err := Reset(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(ctx, cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := secrets.NewSettingsStore(db, key)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	owner := policy.ActorScope{ActorID: ownerSeedID, HouseholdID: HouseholdID, Role: "owner"}
	want := secrets.ProviderConfig{ProviderID: providers.ProviderOpenAI, Model: "gpt-5.4-mini", BaseURL: "https://api.openai.com/v1", APIKey: "demo-provider-key"}
	if err := store.ReplaceProvider(ctx, owner, want, func(context.Context, secrets.ProviderConfig) error { return nil }); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var before []byte
	if err := db.QueryRowContext(ctx, `SELECT encrypted_api_key FROM household_openai_settings WHERE household_id=?`, HouseholdID).Scan(&before); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	if _, err := Reset(ctx, cfg); err != nil {
		t.Fatalf("repeat reset: %v", err)
	}
	db, err = database.Open(ctx, cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err = secrets.NewSettingsStore(db, key)
	if err != nil {
		t.Fatal(err)
	}
	details, err := store.ProviderDetails(ctx, owner)
	if err != nil || details.ProviderID != want.ProviderID || details.Model != want.Model || details.BaseURL != want.BaseURL {
		t.Fatalf("provider details=%+v err=%v", details, err)
	}
	config, err := store.ProviderConfig(ctx, owner)
	if err != nil || config != want {
		t.Fatalf("provider config=%+v err=%v", config, err)
	}
	var after []byte
	if err := db.QueryRowContext(ctx, `SELECT encrypted_api_key FROM household_openai_settings WHERE household_id=?`, HouseholdID).Scan(&after); err != nil || string(after) != string(before) {
		t.Fatalf("provider ciphertext changed=%t err=%v", string(after) != string(before), err)
	}
}

func TestResetRotatesMarkedJudgeEmails(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := Config{
		DatabasePath: filepath.Join(root, "data", "mithra.sqlite3"),
		SourceRoot:   filepath.Join(root, "data", "sources"),
		BackupRoot:   filepath.Join(root, "backups"),
		OwnerEmail:   "old-owner@example.com",
		PartnerEmail: "old-partner@example.com",
		MasterKey:    testKey(),
	}
	seedUnrelatedHousehold(t, ctx, cfg.DatabasePath)
	if _, err := Reset(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(ctx, cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE users SET status='disabled',disabled_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), partnerSeedID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	cfg.OwnerEmail, cfg.PartnerEmail = "judge-owner@example.com", "judge-partner@example.com"
	cfg.OwnerPassword, cfg.PartnerPassword = []byte("owner secure password"), []byte("partner secure password")
	if _, err := Reset(ctx, cfg); err != nil {
		t.Fatalf("rotate marked emails: %v", err)
	}
	db, err = database.Open(ctx, cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, account := range []struct{ id, email, role string }{
		{ownerSeedID, cfg.OwnerEmail, "owner"},
		{partnerSeedID, cfg.PartnerEmail, "adult"},
	} {
		var email, status, household, role string
		if err := db.QueryRowContext(ctx, `SELECT u.email,u.status,m.household_id,m.role FROM users u JOIN household_members m ON m.user_id=u.id WHERE u.id=?`, account.id).Scan(&email, &status, &household, &role); err != nil || email != account.email || status != "active" || household != HouseholdID || role != account.role {
			t.Fatalf("rotated account %s email=%q status=%q household=%q role=%q err=%v", account.id, email, status, household, role, err)
		}
	}
	var oldEmails int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE email IN ('old-owner@example.com','old-partner@example.com')`).Scan(&oldEmails); err != nil || oldEmails != 0 {
		t.Fatalf("old demo emails=%d err=%v", oldEmails, err)
	}
	assertFixtureAndUnrelated(t, ctx, cfg.DatabasePath)
}

func TestResetRefusesMarkedEmailCollisionOrUnrelatedMembership(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := Config{
		DatabasePath: filepath.Join(root, "data", "mithra.sqlite3"),
		SourceRoot:   filepath.Join(root, "data", "sources"),
		BackupRoot:   filepath.Join(root, "backups"),
		OwnerEmail:   "old-owner@example.com",
		PartnerEmail: "old-partner@example.com",
		MasterKey:    testKey(),
	}
	seedUnrelatedHousehold(t, ctx, cfg.DatabasePath)
	if _, err := Reset(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.OwnerEmail = "unrelated@example.com"
	if _, err := Reset(ctx, cfg); !errors.Is(err, ErrUnsafeReset) {
		t.Fatalf("colliding email reset=%v", err)
	}
	db, err := database.Open(ctx, cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `UPDATE household_members SET household_id=?,role='adult',created_at=? WHERE user_id=?`, "unrelated-household", stamp, ownerSeedID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	cfg.OwnerEmail = "judge-owner@example.com"
	if _, err := Reset(ctx, cfg); !errors.Is(err, ErrUnsafeReset) {
		t.Fatalf("unrelated membership reset=%v", err)
	}
	db, err = database.Open(ctx, cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var email string
	if err := db.QueryRowContext(ctx, `SELECT email FROM users WHERE id=?`, ownerSeedID).Scan(&email); err != nil || email != "old-owner@example.com" {
		t.Fatalf("refused reset changed owner email=%q err=%v", email, err)
	}
}

type stateSnapshot struct {
	tableCounts   [5]int
	sourceDigests []string
}

func snapshotState(t *testing.T, ctx context.Context, databasePath, sourceRoot string) stateSnapshot {
	t.Helper()
	db, err := database.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var snapshot stateSnapshot
	for index, table := range []string{"sources", "document_imports", "finance_spending", "health_observations", "planning_events"} {
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE household_id=?`, HouseholdID).Scan(&snapshot.tableCounts[index]); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(sourceRoot, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(raw)
		snapshot.sourceDigests = append(snapshot.sourceDigests, fmt.Sprintf("%x", digest[:]))
	}
	sort.Strings(snapshot.sourceDigests)
	return snapshot
}

func seedUnrelatedHousehold(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	db, err := database.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email,status,password_hash,created_at,updated_at) VALUES('unrelated-owner','unrelated@example.com','active','hash',?,?); INSERT INTO households(id,status,owner_user_id,created_at,updated_at) VALUES('unrelated-household','active','unrelated-owner',?,?); INSERT INTO household_members(household_id,user_id,role,created_at) VALUES('unrelated-household','unrelated-owner','owner',?)`, stamp, stamp, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

func assertFixtureAndUnrelated(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	db, err := database.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var fixture, unrelated, comparable, mismatchKinds, weights, heartRates, sharedSpending, planningEvents int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM demo_households WHERE household_id=? AND fixture_version=?`, HouseholdID, FixtureVersion).Scan(&fixture); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM households WHERE id='unrelated-household'`).Scan(&unrelated); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM health_observations WHERE household_id=? AND reference_context='fasting' AND unit='mg/dL'`, HouseholdID).Scan(&comparable); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT unit) FROM health_observations WHERE household_id=? AND reference_context='post-meal'`, HouseholdID).Scan(&mismatchKinds); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM health_observations WHERE household_id=? AND analyte='Weight'`, HouseholdID).Scan(&weights); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM health_observations WHERE household_id=? AND analyte='Resting heart rate'`, HouseholdID).Scan(&heartRates); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM finance_spending WHERE household_id=? AND visibility='shared'`, HouseholdID).Scan(&sharedSpending); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM planning_events WHERE household_id=? AND visibility='shared'`, HouseholdID).Scan(&planningEvents); err != nil {
		t.Fatal(err)
	}
	if fixture != 1 || unrelated != 1 || comparable != 4 || mismatchKinds != 2 || weights != 4 || heartRates != 4 || sharedSpending != 29 || planningEvents != 6 {
		t.Fatalf("fixture=%d unrelated=%d fasting=%d mismatch-kinds=%d weights=%d heart-rates=%d spending=%d planning=%d", fixture, unrelated, comparable, mismatchKinds, weights, heartRates, sharedSpending, planningEvents)
	}
}

func testKey() []byte {
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	return key
}
