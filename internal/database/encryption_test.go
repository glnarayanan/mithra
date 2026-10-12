package database_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glnarayanan/mithra/internal/database"
)

func testMasterKey(seed byte) []byte {
	return bytes.Repeat([]byte{seed}, 32)
}

func requireSQLCipher(t *testing.T) {
	t.Helper()
	if !database.EncryptionAvailable() {
		t.Skip("binary is not linked against SQLCipher; the container build runs this test")
	}
}

func TestDeriveKeyIsStableAndSeparatedFromTheMasterKey(t *testing.T) {
	first, err := database.DeriveKey(testMasterKey(7))
	if err != nil || len(first) != database.KeyBytes {
		t.Fatalf("derive key = %d bytes, %v", len(first), err)
	}
	again, _ := database.DeriveKey(testMasterKey(7))
	other, _ := database.DeriveKey(testMasterKey(8))
	if !bytes.Equal(first, again) || bytes.Equal(first, other) || bytes.Equal(first, testMasterKey(7)) {
		t.Fatal("database key must be deterministic, master-key specific and never the master key itself")
	}
	if _, err := database.DeriveKey(make([]byte, 31)); err == nil {
		t.Fatal("short master key accepted")
	}
}

func TestKeyParameterUsesTheRawKeyForm(t *testing.T) {
	if database.KeyParameter(nil) != "" {
		t.Fatal("nil key must add no parameter")
	}
	parameter := database.KeyParameter(bytes.Repeat([]byte{0xab}, 32))
	if parameter != "key=x%27"+strings.Repeat("AB", 32)+"%27" {
		t.Fatalf("key parameter = %q", parameter)
	}
	if got := database.FileDSN("/tmp/x.db", "mode=ro", nil); got != "file:/tmp/x.db?mode=ro" {
		t.Fatalf("plain DSN = %q", got)
	}
	if got := database.FileDSN("/tmp/x.db", "", []byte{1}); got != "file:/tmp/x.db?key=x%2701%27" {
		t.Fatalf("keyed DSN = %q", got)
	}
}

func TestIsPlaintextRecognisesOnlyAnUnencryptedHeader(t *testing.T) {
	directory := t.TempDir()
	if plaintext, err := database.IsPlaintext(filepath.Join(directory, "missing.db")); err != nil || plaintext {
		t.Fatalf("missing file = %t, %v", plaintext, err)
	}
	empty := filepath.Join(directory, "empty.db")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if plaintext, err := database.IsPlaintext(empty); err != nil || plaintext {
		t.Fatalf("empty file = %t, %v", plaintext, err)
	}
	random := filepath.Join(directory, "random.db")
	if err := os.WriteFile(random, bytes.Repeat([]byte{0x5a}, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	if plaintext, err := database.IsPlaintext(random); err != nil || plaintext {
		t.Fatalf("random bytes = %t, %v", plaintext, err)
	}
	plain := filepath.Join(directory, "plain.db")
	db, err := database.Open(context.Background(), plain)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if plaintext, err := database.IsPlaintext(plain); err != nil || !plaintext {
		t.Fatalf("plain SQLite = %t, %v", plaintext, err)
	}
	// An existing plain install keeps opening without a key.
	if key, err := database.KeyForFile(plain, testMasterKey(1)); err != nil || key != nil {
		t.Fatalf("plain file key = %x, %v", key, err)
	}
}

func TestOpenWithKeyRefusesWhenSQLCipherIsMissing(t *testing.T) {
	if database.EncryptionAvailable() {
		t.Skip("SQLCipher build")
	}
	key, _ := database.DeriveKey(testMasterKey(3))
	path := filepath.Join(t.TempDir(), "mithra.sqlite3")
	if _, err := database.OpenWithKey(context.Background(), path, key); !errors.Is(err, database.ErrEncryptionUnavailable) {
		t.Fatalf("keyed open without SQLCipher = %v, want ErrEncryptionUnavailable", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("refused keyed open must not create a plaintext database")
	}
	if key, err := database.KeyForFile(path, testMasterKey(3)); err != nil || key != nil {
		t.Fatalf("KeyForFile without SQLCipher = %x, %v", key, err)
	}
}

func TestEncryptedDatabaseKeepsEveryPageUnreadableWithoutTheKey(t *testing.T) {
	requireSQLCipher(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mithra.sqlite3")
	key, err := database.KeyForFile(path, testMasterKey(4))
	if err != nil || key == nil {
		t.Fatalf("new file key = %x, %v", key, err)
	}
	db, err := database.OpenWithKey(ctx, path, key)
	if err != nil {
		t.Fatalf("create encrypted database: %v", err)
	}
	const marker = "blood-pressure-marker-7d1f"
	stamp := "2026-10-12T00:00:00Z"
	if _, err := db.Exec(`INSERT INTO users(id,email,status,password_hash,created_at,updated_at) VALUES('owner',?,'active','',?,?)`, marker+"@example.com", stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, path + "-wal"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		if bytes.Contains(raw, []byte(marker)) || bytes.HasPrefix(raw, []byte("SQLite format 3")) {
			t.Fatalf("%s contains plaintext", filepath.Base(name))
		}
	}
	if plaintext, err := database.IsPlaintext(path); err != nil || plaintext {
		t.Fatalf("encrypted file reported plaintext=%t, %v", plaintext, err)
	}

	reopened, err := database.OpenWithKey(ctx, path, key)
	if err != nil {
		t.Fatalf("reopen with key: %v", err)
	}
	var email string
	if err := reopened.QueryRow(`SELECT email FROM users WHERE id='owner'`).Scan(&email); err != nil || email != marker+"@example.com" {
		t.Fatalf("read back = %q, %v", email, err)
	}
	_ = reopened.Close()

	wrong, _ := database.DeriveKey(testMasterKey(5))
	if _, err := database.OpenWithKey(ctx, path, wrong); !errors.Is(err, database.ErrDatabaseKey) {
		t.Fatalf("wrong key = %v, want ErrDatabaseKey", err)
	}
	if _, err := database.Open(ctx, path); err == nil {
		t.Fatal("encrypted database opened without a key")
	}
	raw, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var count int
	if err := raw.QueryRow(`SELECT count(*) FROM sqlite_master`).Scan(&count); err == nil {
		t.Fatal("plain SQLite read the encrypted schema")
	}
}

func TestOpenWithKeyRefusesAnExistingPlaintextDatabase(t *testing.T) {
	requireSQLCipher(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mithra.sqlite3")
	db, err := database.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	key, _ := database.DeriveKey(testMasterKey(6))
	if _, err := database.OpenWithKey(ctx, path, key); !errors.Is(err, database.ErrPlaintextDatabase) {
		t.Fatalf("keyed open of plaintext = %v, want ErrPlaintextDatabase", err)
	}
}
