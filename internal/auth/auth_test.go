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

func TestAllowlistBootstrapInvitationAndImmediateRevocation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	db := testDB(t)
	service := New(db, Config{Now: func() time.Time { return now }, Token: tokens()})
	if err := service.SynchronizeAllowlist(ctx, []string{"Alice@example.com", "bob@example.com", "carol@example.com"}); err != nil {
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
		t.Fatalf("first adult did not own one-person household: %#v", alice.Scope)
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
	carolReset := resetFor(t, ctx, service, "carol@example.com", "carol-ip")
	if _, err := service.SetPassword(ctx, carolReset, "third adult password", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateInvitation(ctx, alice.Scope, "carol@example.com", time.Hour); err == nil {
		t.Fatal("third adult invitation unexpectedly succeeded")
	}
	if err := service.SynchronizeAllowlist(ctx, []string{"bob@example.com", "carol@example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, alice.Cookie); err != ErrSession {
		t.Fatalf("removed account retained session: %v", err)
	}
	if _, err := service.Authenticate(ctx, bob.Cookie); err != ErrSession {
		t.Fatalf("remaining adult retained session in ownerless household: %v", err)
	}
	var status, householdStatus string
	if err := db.QueryRow(`SELECT status FROM users WHERE email='alice@example.com'`).Scan(&status); err != nil || status != "disabled" {
		t.Fatalf("alice status = %q, %v", status, err)
	}
	if err := db.QueryRow(`SELECT status FROM households WHERE id=?`, alice.Scope.HouseholdID).Scan(&householdStatus); err != nil || householdStatus != "closed" {
		t.Fatalf("household state = %q, %v", householdStatus, err)
	}
	if err := service.RecoverOwner(ctx, alice.Scope.HouseholdID, "bob@example.com"); err != nil {
		t.Fatalf("promote remaining adult: %v", err)
	}
	if _, err := service.Authenticate(ctx, bob.Cookie); err != ErrSession {
		t.Fatalf("recovery revived a revoked cookie: %v", err)
	}
}

func TestSoleOwnerRemovalClosesHousehold(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	db := testDB(t)
	service := New(db, Config{Now: func() time.Time { return now }, Token: tokens()})
	if err := service.SynchronizeAllowlist(ctx, []string{"owner@example.com"}); err != nil {
		t.Fatal(err)
	}
	owner, err := service.SetPassword(ctx, resetFor(t, ctx, service, "owner@example.com", "owner-ip"), "long enough password", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SynchronizeAllowlist(ctx, nil); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM households WHERE id=?`, owner.Scope.HouseholdID).Scan(&status); err != nil || status != "closed" {
		t.Fatalf("sole-owner household = %q, %v", status, err)
	}
	if err := service.SynchronizeAllowlist(ctx, []string{"owner@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := service.RecoverOwner(ctx, owner.Scope.HouseholdID, "owner@example.com"); err != nil {
		t.Fatalf("recover reallowlisted owner: %v", err)
	}
	if _, err := service.Login(ctx, "owner@example.com", "long enough password", "pre-bootstrap-login"); err != ErrInvalidCredentials {
		t.Fatalf("pending recovered owner logged in with retired password: %v", err)
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
	if err := service.SynchronizeAllowlist(ctx, []string{"owner@example.com"}); err != nil {
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
