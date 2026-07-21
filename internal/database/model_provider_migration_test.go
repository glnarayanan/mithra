package database_test

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/glnarayanan/mithra/internal/database"
)

func TestModelProviderMigrationKeepsExistingOpenAISettings(t *testing.T) {
	ctx := context.Background()
	set, err := database.EmbeddedMigrations()
	if err != nil || len(set) < 15 {
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
	ciphertext := bytes.Repeat([]byte{0x42}, 64)
	if _, err := old.Exec(`INSERT INTO household_openai_settings(household_id,encrypted_api_key,key_fingerprint,updated_by_user_id,version,created_at,updated_at) VALUES('h',?,'0123456789abcdef','u',1,'now','now')`, ciphertext); err != nil {
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
	var columns int
	if err := current.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('household_openai_settings') WHERE name IN ('provider_id','provider_model','provider_base_url')`).Scan(&columns); err != nil || columns != 3 {
		t.Fatalf("provider columns = %d, %v", columns, err)
	}
	var provider, model, base string
	var retained []byte
	if err := current.QueryRow(`SELECT provider_id,provider_model,provider_base_url,encrypted_api_key FROM household_openai_settings WHERE household_id='h'`).Scan(&provider, &model, &base, &retained); err != nil || provider != "openai" || model != "gpt-5.4-mini" || base != "https://api.openai.com/v1" || !bytes.Equal(retained, ciphertext) {
		t.Fatalf("upgraded settings = %q %q %q retained=%t err=%v", provider, model, base, bytes.Equal(retained, ciphertext), err)
	}
}
