package installer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/imports"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/storage"
)

func TestEncryptedDatabaseBackupAndRestoreStayCiphertext(t *testing.T) {
	if !database.EncryptionAvailable() {
		t.Skip("binary is not linked against SQLCipher; the container build runs this test")
	}
	ctx := context.Background()
	key := bytes.Repeat([]byte{0x42}, 32)
	paths := OwnedPaths(t.TempDir(), AppOnly)
	if err := os.MkdirAll(paths.Data, 0o700); err != nil {
		t.Fatal(err)
	}
	databaseKey, err := database.KeyForFile(paths.Database, key)
	if err != nil || databaseKey == nil {
		t.Fatalf("new database key = %x, %v", databaseKey, err)
	}
	db, err := database.OpenWithKey(ctx, paths.Database, databaseKey)
	if err != nil {
		t.Fatal(err)
	}
	const marker = "hba1c-marker-5c2e"
	stamp := "2026-10-12T00:00:00Z"
	for _, statement := range []string{
		`INSERT INTO users(id,email,status,password_hash,created_at,updated_at) VALUES('owner','owner@example.com','active','hash','` + stamp + `','` + stamp + `')`,
		`INSERT INTO households(id,status,owner_user_id,created_at,updated_at) VALUES('home','active','owner','` + stamp + `','` + stamp + `')`,
		`INSERT INTO household_members(household_id,user_id,role,created_at) VALUES('home','owner','owner','` + stamp + `')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	store, err := storage.New(db, paths.Sources, key)
	if err != nil {
		t.Fatal(err)
	}
	scope := policy.ActorScope{ActorID: "owner", HouseholdID: "home", Role: "owner"}
	if _, err := store.Store(ctx, scope, []byte(marker), storage.Metadata{Family: "csv", Version: 1, Visibility: policy.Shared, LocatorKind: "source", LocatorValue: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, err := imports.NewDeletionJournal(paths.Journal, key); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	paths.Backups = t.TempDir()
	archive, err := CreateBackup(ctx, paths, key, time.Date(2026, 10, 12, 3, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("backup encrypted database: %v", err)
	}
	if err := VerifyBackupArchive(archive, key); err != nil {
		t.Fatalf("verify encrypted backup: %v", err)
	}
	if err := DatabasePreflightWithKey(ctx, paths.Database, key); err != nil {
		t.Fatalf("preflight with key: %v", err)
	}
	if err := DatabasePreflight(ctx, paths.Database); err == nil {
		t.Fatal("encrypted database passed preflight without the key")
	}

	stage := t.TempDir()
	if err := extractArchive(archive, stage, key); err != nil {
		t.Fatal(err)
	}
	snapshot, err := os.ReadFile(filepath.Join(stage, "mithra.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasPrefix(snapshot, []byte("SQLite format 3")) || bytes.Contains(snapshot, []byte("owner@example.com")) {
		t.Fatal("database snapshot inside the backup is plaintext")
	}

	target := OwnedPaths(t.TempDir(), AppOnly)
	if err := os.MkdirAll(target.Data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RestoreBackup(ctx, target, archive, key, []string{"owner@example.com"}, func() error {
		return DatabasePreflightWithKey(ctx, target.Database, key)
	}); err != nil {
		t.Fatalf("restore encrypted backup: %v", err)
	}
	if plaintext, err := database.IsPlaintext(target.Database); err != nil || plaintext {
		t.Fatalf("restored database plaintext=%t err=%v", plaintext, err)
	}
	restored, err := database.OpenWithKey(ctx, target.Database, databaseKey)
	if err != nil {
		t.Fatalf("open restored database: %v", err)
	}
	defer restored.Close()
	var status string
	if err := restored.QueryRow(`SELECT status FROM users WHERE id='owner'`).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("restored owner status=%q err=%v", status, err)
	}
}
