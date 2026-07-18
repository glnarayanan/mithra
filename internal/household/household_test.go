package household

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/database"
)

func TestRecoveryRejectsDisabledAndForeignCandidates(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	db := householdDB(t)
	service := New(db, Config{Now: func() time.Time { return now }})
	if err := service.SyncAllowlist(ctx, []string{"former@example.com", "recovery@example.com", "foreign@example.com"}); err != nil {
		t.Fatal(err)
	}
	stamp := now.Format(time.RFC3339Nano)
	if _, err := db.Exec(`UPDATE users SET status='active' WHERE email IN ('recovery@example.com','foreign@example.com')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO households(id,status,owner_user_id,created_at,updated_at) SELECT 'home','active',id,?,? FROM users WHERE email='former@example.com'`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO household_members(household_id,user_id,role,created_at) SELECT 'home',id,'owner',? FROM users WHERE email='former@example.com'`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO households(id,status,owner_user_id,created_at,updated_at) SELECT 'foreign','active',id,?,? FROM users WHERE email='foreign@example.com'`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO household_members(household_id,user_id,role,created_at) SELECT 'foreign',id,'owner',? FROM users WHERE email='foreign@example.com'`, stamp); err != nil {
		t.Fatal(err)
	}
	if err := service.SyncAllowlist(ctx, []string{"recovery@example.com", "foreign@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := service.RecoverOwner(ctx, "home", "former@example.com"); err == nil {
		t.Fatal("disabled former owner recovered the household")
	}
	if err := service.RecoverOwner(ctx, "home", "foreign@example.com"); err == nil {
		t.Fatal("foreign-household adult recovered another household")
	}
	if err := service.RecoverOwner(ctx, "home", "recovery@example.com"); err != nil {
		t.Fatal(err)
	}
	var status, email string
	if err := db.QueryRow(`SELECT h.status,u.email FROM households h JOIN users u ON u.id=h.owner_user_id WHERE h.id='home'`).Scan(&status, &email); err != nil || status != "active" || email != "recovery@example.com" {
		t.Fatalf("recovery = %q, %q, %v", status, email, err)
	}
}

func TestRecoveryReopensForReallowlistedFormerOwnerWithoutSkippingBootstrap(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	db := householdDB(t)
	service := New(db, Config{Now: func() time.Time { return now }})
	if err := service.SyncAllowlist(ctx, []string{"former@example.com"}); err != nil {
		t.Fatal(err)
	}
	stamp := now.Format(time.RFC3339Nano)
	if _, err := db.Exec(`UPDATE users SET status='active',password_hash='retired-hash' WHERE email='former@example.com'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO households(id,status,owner_user_id,created_at,updated_at) SELECT 'home','active',id,?,? FROM users WHERE email='former@example.com'`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO household_members(household_id,user_id,role,created_at) SELECT 'home',id,'owner',? FROM users WHERE email='former@example.com'`, stamp); err != nil {
		t.Fatal(err)
	}
	if err := service.SyncAllowlist(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := service.SyncAllowlist(ctx, []string{"former@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := service.RecoverOwner(ctx, "home", "former@example.com"); err != nil {
		t.Fatalf("recover reallowlisted former owner: %v", err)
	}
	var householdStatus, userStatus, ownerEmail string
	if err := db.QueryRow(`SELECT h.status,u.status,u.email FROM households h JOIN users u ON u.id=h.owner_user_id WHERE h.id='home'`).Scan(&householdStatus, &userStatus, &ownerEmail); err != nil {
		t.Fatal(err)
	}
	if householdStatus != "active" || userStatus != "pending" || ownerEmail != "former@example.com" {
		t.Fatalf("recovery state = household %q user %q owner %q", householdStatus, userStatus, ownerEmail)
	}
}

func householdDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "mithra.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
