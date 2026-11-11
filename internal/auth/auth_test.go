package auth

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/database"
)

func TestOwnerBootstrapInvitationAndImmediateRemoval(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	db := testDB(t)
	service := New(db, Config{Now: func() time.Time { return now }, Token: tokens()})
	if err := service.ConfigureOwner(ctx, "Alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if delivery, err := service.RequestPasswordReset(ctx, "nobody@example.com", "ip"); err != nil || delivery != nil {
		t.Fatalf("unknown reset = %#v, %v", delivery, err)
	}
	aliceReset := resetFor(t, ctx, service, "alice@example.com", "alice-ip")
	alice, err := service.SetPassword(ctx, aliceReset, "a secure password", "")
	if err != nil {
		t.Fatal(err)
	}
	if alice.Scope.Role != "owner" || alice.Scope.HouseholdID == "" {
		t.Fatalf("configured owner did not own one-person household: %#v", alice.Scope)
	}
	if err := service.VerifyCSRF(ctx, alice.Cookie, alice.CSRF); err != nil {
		t.Fatalf("csrf: %v", err)
	}
	if err := service.VerifyCSRF(ctx, alice.Cookie, "wrong"); err != ErrCSRF {
		t.Fatalf("wrong csrf = %v", err)
	}
	assertNoPlaintext(t, db, aliceReset)
	invite, err := service.CreateInvitation(ctx, alice.Scope, "bob@example.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// An invitee uses the invitation, not a reset link, and cannot start a
	// household of their own.
	if delivery, err := service.RequestPasswordReset(ctx, "bob@example.com", "bob-ip"); err != nil || delivery != nil {
		t.Fatalf("invitee reset = %#v, %v", delivery, err)
	}
	bob, err := service.SetPassword(ctx, "", "another secure password", invite.Token)
	if err != nil {
		t.Fatal(err)
	}
	if bob.Scope.HouseholdID != alice.Scope.HouseholdID || bob.Scope.Role != "adult" {
		t.Fatalf("invited adult scope = %#v", bob.Scope)
	}
	if _, err := service.SetPassword(ctx, "", "another secure password", invite.Token); err != ErrInvalidReset {
		t.Fatalf("reused invitation password setup = %v", err)
	}
	assertNoPlaintext(t, db, invite.Token)
	if _, err := service.CreateInvitation(ctx, alice.Scope, "carol@example.com", time.Hour); err == nil {
		t.Fatal("third adult invitation unexpectedly succeeded")
	}
	if _, err := service.CreateInvitation(ctx, bob.Scope, "carol@example.com", time.Hour); err == nil {
		t.Fatal("partner sent an invitation")
	}
	access, err := service.Access(ctx, alice.Scope)
	if err != nil || len(access) != 2 || access[1].Email != "bob@example.com" || access[1].State != "active" {
		t.Fatalf("access = %#v, %v", access, err)
	}
	if err := service.RemovePartner(ctx, alice.Scope, "bob@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, bob.Cookie); err != ErrSession {
		t.Fatalf("removed partner retained session: %v", err)
	}
	if _, err := service.Login(ctx, "bob@example.com", "another secure password", "bob-login"); err != ErrInvalidCredentials {
		t.Fatalf("removed partner signed in: %v", err)
	}
	if _, err := service.Authenticate(ctx, alice.Cookie); err != nil {
		t.Fatalf("owner lost session when removing partner: %v", err)
	}
	again, err := service.CreateInvitation(ctx, alice.Scope, "bob@example.com", time.Hour)
	if err != nil {
		t.Fatalf("invite removed partner again: %v", err)
	}
	returned, err := service.SetPassword(ctx, "", "a returning password", again.Token)
	if err != nil {
		t.Fatalf("returning partner: %v", err)
	}
	if returned.Scope.HouseholdID != alice.Scope.HouseholdID || returned.Scope.Role != "adult" {
		t.Fatalf("returning partner scope = %#v", returned.Scope)
	}
	if _, err := service.SetPassword(ctx, "", "a returning password", again.Token); err != ErrInvalidReset {
		t.Fatalf("returning invitation reused = %v", err)
	}
}

func TestOnlyTheConfiguredOwnerStartsTheHousehold(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	db := testDB(t)
	service := New(db, Config{Now: func() time.Time { return now }, Token: tokens()})
	stamp := now.Format(time.RFC3339Nano)
	// A pending account left from an older allowlist cannot start a second
	// household or get a reset link.
	if _, err := db.Exec(`INSERT INTO users(id,email,status,created_at,updated_at) VALUES('legacy','legacy@example.com','pending',?1,?1)`, stamp); err != nil {
		t.Fatal(err)
	}
	if err := service.ConfigureOwner(ctx, "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if delivery, err := service.RequestPasswordReset(ctx, "legacy@example.com", "legacy-ip"); err != nil || delivery != nil {
		t.Fatalf("legacy pending reset = %#v, %v", delivery, err)
	}
	owner, err := service.SetPassword(ctx, resetFor(t, ctx, service, "owner@example.com", "owner-ip"), "long enough password", "")
	if err != nil {
		t.Fatal(err)
	}
	if exists, err := service.HasHousehold(ctx); err != nil || !exists {
		t.Fatalf("household after owner sign-in = %t, %v", exists, err)
	}
	// Changing the configured owner later does not open a second household.
	if err := service.ConfigureOwner(ctx, "new-owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if delivery, err := service.RequestPasswordReset(ctx, "new-owner@example.com", "new-ip"); err != nil || delivery == nil {
		t.Fatalf("new owner reset = %#v, %v", delivery, err)
	} else if _, err := service.SetPassword(ctx, delivery.Token, "another long password", ""); err == nil {
		t.Fatal("second household started")
	}
	var households int
	if err := db.QueryRow(`SELECT COUNT(*) FROM households`).Scan(&households); err != nil || households != 1 {
		t.Fatalf("households = %d, %v", households, err)
	}
	// The original owner is unaffected.
	if _, err := service.Authenticate(ctx, owner.Cookie); err != nil {
		t.Fatalf("owner session: %v", err)
	}
	// Recovery still works for a closed household from an older release.
	if _, err := db.Exec(`UPDATE households SET status='closed',owner_user_id=NULL`); err != nil {
		t.Fatal(err)
	}
	if err := service.RecoverOwner(ctx, owner.Scope.HouseholdID, "owner@example.com"); err != nil {
		t.Fatalf("recover owner: %v", err)
	}
	recovered, err := service.SetPassword(ctx, resetFor(t, ctx, service, "owner@example.com", "recovery-ip"), "new recovery password", "")
	if err != nil {
		t.Fatalf("bootstrap recovered owner: %v", err)
	}
	if recovered.Scope.HouseholdID != owner.Scope.HouseholdID || recovered.Scope.Role != "owner" {
		t.Fatalf("recovered owner scope = %#v", recovered.Scope)
	}
}

func TestPasswordAndSessionRotation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	db := testDB(t)
	service := New(db, Config{Now: func() time.Time { return now }, Token: tokens()})
	if err := service.ConfigureOwner(ctx, "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	reset := resetFor(t, ctx, service, "owner@example.com", "owner-ip")
	if _, err := service.SetPassword(ctx, reset, "short", ""); err != ErrPassword {
		t.Fatalf("short password = %v", err)
	}
	first, err := service.SetPassword(ctx, reset, "long enough password", "")
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := service.RotateSession(ctx, first.Cookie)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Cookie == first.Cookie || rotated.CSRF == first.CSRF {
		t.Fatal("rotation did not replace browser secrets")
	}
	if _, err := service.Authenticate(ctx, first.Cookie); err != ErrSession {
		t.Fatalf("fixed session remained valid: %v", err)
	}
	if _, err := service.Authenticate(ctx, rotated.Cookie); err != nil {
		t.Fatalf("rotated session: %v", err)
	}
	if _, err := service.SetPassword(ctx, reset, "another password long", ""); err != ErrInvalidReset {
		t.Fatalf("reused reset = %v", err)
	}
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "mithra.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
func resetFor(t *testing.T, ctx context.Context, service *Service, email, key string) string {
	t.Helper()
	delivery, err := service.RequestPasswordReset(ctx, email, key)
	if err != nil || delivery == nil {
		t.Fatalf("reset for %s: %#v, %v", email, delivery, err)
	}
	return delivery.Token
}
func tokens() func() (string, error) {
	next := 0
	return func() (string, error) { next++; return fmt.Sprintf("test-token-%03d", next), nil }
}
func assertNoPlaintext(t *testing.T, db *sql.DB, raw string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM password_reset_tokens WHERE token_hash=?`, raw).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("raw reset token persisted")
	}
}
