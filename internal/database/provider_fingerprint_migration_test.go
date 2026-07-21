package database_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/secrets"
)

func TestProviderFingerprintMigrationRemovesSecretDerivedMetadata(t *testing.T) {
	ctx := context.Background()
	set, err := database.EmbeddedMigrations()
	if err != nil || len(set) < 17 {
		t.Fatalf("migrations = %d, %v", len(set), err)
	}
	path := filepath.Join(t.TempDir(), "mithra.sqlite3")
	old, err := database.OpenWithMigrations(ctx, path, set[:len(set)-1])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO users(id,email,status,created_at,updated_at) VALUES('u','owner@example.com','active','now','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO households(id,status,owner_user_id,created_at,updated_at) VALUES('h','active','u','now','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO household_members(household_id,user_id,role,created_at) VALUES('h','u','owner','now')`); err != nil {
		t.Fatal(err)
	}
	masterKey := bytes.Repeat([]byte{0x41}, secrets.MasterKeyBytes)
	box, err := secrets.New(masterKey, secrets.Settings)
	if err != nil {
		t.Fatal(err)
	}
	apiKey := "provider-secret"
	ciphertext, err := box.Seal([]byte(apiKey), []byte("openai-api-key\x00h"))
	if err != nil {
		t.Fatal(err)
	}
	secretDigest := sha256.Sum256([]byte(apiKey))
	oldFingerprint := fmt.Sprintf("%x", secretDigest[:8])
	if _, err := old.Exec(`INSERT INTO household_openai_settings(household_id,encrypted_api_key,key_fingerprint,provider_id,provider_model,provider_base_url,updated_by_user_id,version,created_at,updated_at) VALUES('h',?,?,'openai','gpt-5.4-mini','https://api.openai.com/v1','u',1,'now','now')`, ciphertext, oldFingerprint); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	current, err := database.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	var fingerprint string
	var retained []byte
	if err := current.QueryRow(`SELECT key_fingerprint,encrypted_api_key FROM household_openai_settings WHERE household_id='h'`).Scan(&fingerprint, &retained); err != nil {
		t.Fatal(err)
	}
	if fingerprint == oldFingerprint || len(fingerprint) != 16 || !bytes.Equal(retained, ciphertext) {
		t.Fatalf("fingerprint=%q old=%q ciphertext_retained=%t", fingerprint, oldFingerprint, bytes.Equal(retained, ciphertext))
	}
	store, err := secrets.NewSettingsStore(current, masterKey)
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.ProviderConfig(ctx, policy.ActorScope{ActorID: "u", HouseholdID: "h", Role: "owner"})
	if err != nil || config.APIKey != apiKey {
		t.Fatalf("provider key after migration=%q err=%v", config.APIKey, err)
	}
	scope := policy.ActorScope{ActorID: "u", HouseholdID: "h", Role: "owner"}
	var firstSavedFingerprint string
	for attempt := 0; attempt < 2; attempt++ {
		if err := store.ReplaceProvider(ctx, scope, secrets.ProviderConfig{ProviderID: "openai", Model: "gpt-5.4-mini", APIKey: apiKey}, func(context.Context, secrets.ProviderConfig) error { return nil }); err != nil {
			t.Fatal(err)
		}
		var savedFingerprint string
		var savedCiphertext []byte
		if err := current.QueryRow(`SELECT key_fingerprint,encrypted_api_key FROM household_openai_settings WHERE household_id='h'`).Scan(&savedFingerprint, &savedCiphertext); err != nil {
			t.Fatal(err)
		}
		ciphertextDigest := sha256.Sum256(savedCiphertext)
		if savedFingerprint != fmt.Sprintf("%x", ciphertextDigest[:8]) || savedFingerprint == oldFingerprint {
			t.Fatalf("saved fingerprint=%q ciphertext digest=%x clear-key digest=%q", savedFingerprint, ciphertextDigest[:8], oldFingerprint)
		}
		if attempt == 0 {
			firstSavedFingerprint = savedFingerprint
		} else if savedFingerprint == firstSavedFingerprint {
			t.Fatal("re-saving the provider key retained a deterministic fingerprint")
		}
	}
}
