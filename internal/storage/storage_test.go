package storage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/secrets"
)

func TestEncryptedSourceRoundTripAndPrivacy(t *testing.T) {
	service, db, owner, partner := storageFixture(t)
	plaintext := []byte("annual contribution: 120000")
	metadata := Metadata{Family: "text", Version: 1, LocatorKind: "source", LocatorValue: "message-1"}
	personal, err := service.Store(context.Background(), owner, plaintext, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if personal.Visibility != policy.Personal || !validStorageKey(personal.StorageKey) {
		t.Fatalf("personal source = %#v", personal)
	}
	ciphertext, err := os.ReadFile(filepath.Join(service.root, personal.StorageKey+".enc"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, plaintext) {
		t.Fatal("ciphertext exposed plaintext")
	}
	opened, got, err := service.Read(context.Background(), owner, personal.ID)
	if err != nil || !bytes.Equal(opened, plaintext) || got.ID != personal.ID {
		t.Fatalf("read = %q %#v %v", opened, got, err)
	}
	if _, _, err := service.Read(context.Background(), partner, personal.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("partner personal read = %v", err)
	}
	foreign := policy.ActorScope{ActorID: "foreign-user", HouseholdID: "foreign-home", Role: "owner"}
	if _, _, err := service.Read(context.Background(), foreign, personal.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign read = %v", err)
	}

	metadata.Visibility = policy.Shared
	metadata.LocatorValue = "message-2"
	shared, err := service.Store(context.Background(), owner, plaintext, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if shared.StorageKey == personal.StorageKey {
		t.Fatal("source storage key was reused")
	}
	if opened, _, err := service.Read(context.Background(), partner, shared.ID); err != nil || !bytes.Equal(opened, plaintext) {
		t.Fatalf("shared partner read = %q, %v", opened, err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`UPDATE users SET status='disabled',disabled_at=?,updated_at=? WHERE id=?`, stamp, stamp, owner.ActorID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Read(context.Background(), owner, personal.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled stale scope read = %v", err)
	}
	var raw int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sources WHERE plaintext_digest=? OR storage_key=?`, string(plaintext), string(plaintext)).Scan(&raw); err != nil || raw != 0 {
		t.Fatalf("plaintext persisted in metadata: %d, %v", raw, err)
	}
}

func TestEncryptedSourceTamperMissingAndSymlinkFailClosed(t *testing.T) {
	service, _, owner, _ := storageFixture(t)
	store := func(locator string) Source {
		t.Helper()
		source, err := service.Store(context.Background(), owner, []byte("health observation"), Metadata{Family: "pdf", Version: 1, LocatorKind: "page", LocatorValue: locator})
		if err != nil {
			t.Fatal(err)
		}
		return source
	}

	tampered := store("4")
	path := filepath.Join(service.root, tampered.StorageKey+".enc")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content[len(content)-1] ^= 1
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Read(context.Background(), owner, tampered.ID); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("tampered read = %v", err)
	}

	missing := store("5")
	if err := os.Remove(filepath.Join(service.root, missing.StorageKey+".enc")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Read(context.Background(), owner, missing.ID); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("missing read = %v", err)
	}

	symlinked := store("6")
	symlinkPath := filepath.Join(service.root, symlinked.StorageKey+".enc")
	if err := os.Remove(symlinkPath); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(external, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, symlinkPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Read(context.Background(), owner, symlinked.ID); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("symlink read = %v", err)
	}
}

func TestStoreFailureBoundariesLeaveNoBrokenLiveRow(t *testing.T) {
	for _, boundary := range []string{"before rename", "after rename", "before commit"} {
		t.Run(boundary, func(t *testing.T) {
			service, db, owner, _ := storageFixture(t)
			failure := errors.New("injected")
			switch boundary {
			case "before rename":
				service.hooks.beforeRename = func() error { return failure }
			case "after rename":
				service.hooks.afterRename = func() error { return failure }
			case "before commit":
				service.hooks.beforeCommit = func() error { return failure }
			}
			if _, err := service.Store(context.Background(), owner, []byte("planning source"), Metadata{Family: "text", Version: 1, LocatorKind: "source", LocatorValue: boundary}); !errors.Is(err, ErrStorage) {
				t.Fatalf("store error = %v", err)
			}
			var rows int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sources`).Scan(&rows); err != nil || rows != 0 {
				t.Fatalf("live rows = %d, %v", rows, err)
			}
			service.hooks = hooks{}
			if err := service.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(service.root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("orphan entries = %v, %v", entries, err)
			}
		})
	}
}

func TestReconcileRemovesOnlyMithraOrphansAndPreservesReferencedFiles(t *testing.T) {
	service, _, owner, _ := storageFixture(t)
	source, err := service.Store(context.Background(), owner, []byte("source"), Metadata{Family: "csv", Version: 1, LocatorKind: "row", LocatorValue: "2"})
	if err != nil {
		t.Fatal(err)
	}
	orphanKey := strings.Repeat("A", 43)
	stageKey := strings.Repeat("B", 43)
	if err := os.WriteFile(filepath.Join(service.root, orphanKey+".enc"), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(service.root, ".stage-"+stageKey), []byte("stage"), 0o600); err != nil {
		t.Fatal(err)
	}
	unowned := filepath.Join(service.root, "operator-note")
	if err := os.WriteFile(unowned, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(service.root, orphanKey+".enc"), filepath.Join(service.root, ".stage-"+stageKey)} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("orphan remains: %s", path)
		}
	}
	for _, path := range []string{filepath.Join(service.root, source.StorageKey+".enc"), unowned} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("preserved file missing: %s: %v", path, err)
		}
	}
}

func TestStorageRejectsUnsafeRootInputAndDatabaseScope(t *testing.T) {
	db, _, owner, _ := storageDatabase(t)
	master := bytes.Repeat([]byte{7}, secrets.MasterKeyBytes)
	if _, err := New(db, "relative", master); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("relative root = %v", err)
	}
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := New(db, link, master); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("symlink root = %v", err)
	}
	service, err := New(db, filepath.Join(t.TempDir(), "sources"), master)
	if err != nil {
		t.Fatal(err)
	}
	invalid := policy.ActorScope{ActorID: "missing", HouseholdID: owner.HouseholdID, Role: "adult"}
	if _, err := service.Store(context.Background(), invalid, []byte("value"), Metadata{Family: "text", Version: 1, LocatorKind: "source", LocatorValue: "1"}); !errors.Is(err, ErrStorage) {
		t.Fatalf("invalid membership store = %v", err)
	}
}

func storageFixture(t *testing.T) (*Service, *sql.DB, policy.ActorScope, policy.ActorScope) {
	t.Helper()
	db, root, owner, partner := storageDatabase(t)
	service, err := New(db, root, bytes.Repeat([]byte{9}, secrets.MasterKeyBytes))
	if err != nil {
		t.Fatal(err)
	}
	return service, db, owner, partner
}

func storageDatabase(t *testing.T) (*sql.DB, string, policy.ActorScope, policy.ActorScope) {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "mithra.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	statements := []string{
		`INSERT INTO users(id,email,status,password_hash,created_at,updated_at) VALUES('owner','owner@example.com','active','hash',?,?)`,
		`INSERT INTO users(id,email,status,password_hash,created_at,updated_at) VALUES('partner','partner@example.com','active','hash',?,?)`,
		`INSERT INTO households(id,status,owner_user_id,created_at,updated_at) VALUES('home','active','owner',?,?)`,
		`INSERT INTO household_members(household_id,user_id,role,created_at) VALUES('home','owner','owner',?)`,
		`INSERT INTO household_members(household_id,user_id,role,created_at) VALUES('home','partner','adult',?)`,
	}
	for _, statement := range statements {
		arguments := []any{now}
		if strings.Count(statement, "?") == 2 {
			arguments = []any{now, now}
		}
		if _, err := db.Exec(statement, arguments...); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(t.TempDir(), "sources")
	return db, root, policy.ActorScope{ActorID: "owner", HouseholdID: "home", Role: "owner"}, policy.ActorScope{ActorID: "partner", HouseholdID: "home", Role: "adult"}
}
