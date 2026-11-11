package household

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/policy"
)

func TestRecoveryRejectsForeignCandidatesAndReopensForAnotherAdult(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	db := householdDB(t)
	service := New(db, Config{Now: func() time.Time { return now }})
	stamp := now.Format(time.RFC3339Nano)
	for _, statement := range []string{
		`INSERT INTO users(id,email,status,created_at,updated_at) VALUES('former','former@example.com','disabled',?1,?1),('foreign','foreign@example.com','active',?1,?1)`,
		`INSERT INTO households(id,status,owner_user_id,created_at,updated_at) VALUES('home','closed',NULL,?1,?1),('elsewhere','active','foreign',?1,?1)`,
		`INSERT INTO household_members(household_id,user_id,role,created_at) VALUES('home','former','owner',?1),('elsewhere','foreign','owner',?1)`,
	} {
		if _, err := db.Exec(statement, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.RecoverOwner(ctx, "home", "foreign@example.com"); err == nil {
		t.Fatal("foreign-household adult recovered another household")
	}
	if err := service.RecoverOwner(ctx, "elsewhere", "recovery@example.com"); err == nil {
		t.Fatal("recovery reopened an active household")
	}
	if err := service.RecoverOwner(ctx, "home", "recovery@example.com"); err != nil {
		t.Fatal(err)
	}
	var status, email, userStatus string
	if err := db.QueryRow(`SELECT h.status,u.email,u.status FROM households h JOIN users u ON u.id=h.owner_user_id WHERE h.id='home'`).Scan(&status, &email, &userStatus); err != nil || status != "active" || email != "recovery@example.com" || userStatus != "pending" {
		t.Fatalf("recovery = %q, %q, %q, %v", status, email, userStatus, err)
	}
}

func TestRecoveryReopensForFormerOwnerWithoutSkippingBootstrap(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	db := householdDB(t)
	service := New(db, Config{Now: func() time.Time { return now }})
	stamp := now.Format(time.RFC3339Nano)
	for _, statement := range []string{
		`INSERT INTO users(id,email,status,password_hash,created_at,updated_at) VALUES('former','former@example.com','disabled','retired-hash',?1,?1)`,
		`INSERT INTO households(id,status,owner_user_id,created_at,updated_at) VALUES('home','closed',NULL,?1,?1)`,
		`INSERT INTO household_members(household_id,user_id,role,created_at) VALUES('home','former','owner',?1)`,
	} {
		if _, err := db.Exec(statement, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.RecoverOwner(ctx, "home", "former@example.com"); err != nil {
		t.Fatalf("recover former owner: %v", err)
	}
	var householdStatus, userStatus, hash string
	if err := db.QueryRow(`SELECT h.status,u.status,u.password_hash FROM households h JOIN users u ON u.id=h.owner_user_id WHERE h.id='home'`).Scan(&householdStatus, &userStatus, &hash); err != nil {
		t.Fatal(err)
	}
	if householdStatus != "active" || userStatus != "pending" || hash != "" {
		t.Fatalf("recovery state = household %q user %q hash %q", householdStatus, userStatus, hash)
	}
}

func TestEnsureOwnerSeedsOnlyTheConfiguredOwner(t *testing.T) {
	ctx := context.Background()
	db := householdDB(t)
	service := New(db, Config{})
	if err := service.SetOwnerEmail("not an email"); err == nil {
		t.Fatal("malformed owner email accepted")
	}
	if err := service.SetOwnerEmail(" Owner@Example.com "); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := service.EnsureOwner(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var status string
	if err := db.QueryRow(`SELECT COUNT(*),MAX(status) FROM users WHERE email='owner@example.com'`).Scan(&count, &status); err != nil || count != 1 || status != "pending" {
		t.Fatalf("owner seed = %d %q %v", count, status, err)
	}
	if exists, err := service.HasHousehold(ctx); err != nil || exists {
		t.Fatalf("household before sign-in = %t, %v", exists, err)
	}
}

func TestInvitationsAreManagedByTheOwner(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	db := householdDB(t)
	service := New(db, Config{Now: func() time.Time { return now }})
	owner := seedOwner(t, db, now)

	if _, err := service.CreateInvitation(ctx, policy.ActorScope{ActorID: "partner", HouseholdID: "home", Role: "adult"}, "x@example.com", time.Hour); err != ErrNotOwner {
		t.Fatalf("adult invitation = %v", err)
	}
	if _, err := service.CreateInvitation(ctx, owner, "owner@example.com", time.Hour); err != ErrAlreadyMember {
		t.Fatalf("owner self-invitation = %v", err)
	}
	if _, err := service.CreateInvitation(ctx, owner, "Partner@Example.com", 7*24*time.Hour); err != nil {
		t.Fatalf("invite new address: %v", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM users WHERE email='partner@example.com'`).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("invited account = %q, %v", status, err)
	}
	access, err := service.Access(ctx, "home")
	if err != nil || len(access) != 2 || access[0].Role != "owner" || access[1].Role != "invited" || access[1].State != "invited" {
		t.Fatalf("access with invitation = %#v, %v", access, err)
	}
	now = now.Add(8 * 24 * time.Hour)
	if access, _ := service.Access(ctx, "home"); access[1].State != "expired" {
		t.Fatalf("expired invitation state = %q", access[1].State)
	}
	if err := service.CancelInvitation(ctx, owner, "partner@example.com"); err != nil {
		t.Fatalf("cancel invitation: %v", err)
	}
	if err := service.CancelInvitation(ctx, owner, "partner@example.com"); err != ErrNoInvitation {
		t.Fatalf("second cancel = %v", err)
	}
	if err := db.QueryRow(`SELECT status FROM users WHERE email='partner@example.com'`).Scan(&status); err != nil || status != "disabled" {
		t.Fatalf("cancelled account = %q, %v", status, err)
	}
	if access, _ := service.Access(ctx, "home"); len(access) != 1 {
		t.Fatalf("access after cancel = %#v", access)
	}
	if _, err := service.CreateInvitation(ctx, owner, "partner@example.com", time.Hour); err != nil {
		t.Fatalf("invite again after cancel: %v", err)
	}
	if err := db.QueryRow(`SELECT status FROM users WHERE email='partner@example.com'`).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("re-invited account = %q, %v", status, err)
	}
	// Inviting someone else replaces the open invitation.
	if _, err := service.CreateInvitation(ctx, owner, "second@example.com", time.Hour); err != nil {
		t.Fatalf("invite second address: %v", err)
	}
	if err := db.QueryRow(`SELECT status FROM users WHERE email='partner@example.com'`).Scan(&status); err != nil || status != "disabled" {
		t.Fatalf("replaced invitee account = %q, %v", status, err)
	}
	if access, _ := service.Access(ctx, "home"); len(access) != 2 || access[1].Email != "second@example.com" {
		t.Fatalf("access after replacing invitation = %#v", access)
	}
}

func TestRemovedPartnerKeepsThePlaceUntilInvitedAgain(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	db := householdDB(t)
	service := New(db, Config{Now: func() time.Time { return now }})
	owner := seedOwner(t, db, now)
	stamp := now.Format(time.RFC3339Nano)
	for _, statement := range []string{
		`INSERT INTO users(id,email,status,password_hash,created_at,updated_at) VALUES('partner','partner@example.com','active','hash',?1,?1)`,
		`INSERT INTO household_members(household_id,user_id,role,created_at) VALUES('home','partner','adult',?1)`,
		`INSERT INTO browser_sessions(token_hash,csrf_hash,user_id,expires_at,created_at) VALUES('cookie','csrf','partner','2099-01-01T00:00:00Z',?1)`,
	} {
		if _, err := db.Exec(statement, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.RemovePartner(ctx, owner, "owner@example.com"); err != ErrNotPartner {
		t.Fatalf("owner removed themselves: %v", err)
	}
	if err := service.RemovePartner(ctx, policy.ActorScope{ActorID: "partner", HouseholdID: "home", Role: "adult"}, "partner@example.com"); err != ErrNotOwner {
		t.Fatalf("partner removal by partner = %v", err)
	}
	if err := service.RemovePartner(ctx, owner, "partner@example.com"); err != nil {
		t.Fatalf("remove partner: %v", err)
	}
	var status, hash, revoked string
	if err := db.QueryRow(`SELECT u.status,u.password_hash,COALESCE(s.revoked_at,'') FROM users u JOIN browser_sessions s ON s.user_id=u.id WHERE u.id='partner'`).Scan(&status, &hash, &revoked); err != nil || status != "disabled" || hash != "" || revoked == "" {
		t.Fatalf("removed partner = %q %q %q %v", status, hash, revoked, err)
	}
	if err := service.RemovePartner(ctx, owner, "partner@example.com"); err != ErrNotPartner {
		t.Fatalf("second removal = %v", err)
	}
	access, err := service.Access(ctx, "home")
	if err != nil || len(access) != 2 || access[1].State != "removed" {
		t.Fatalf("access after removal = %#v, %v", access, err)
	}
	if _, err := service.CreateInvitation(ctx, owner, "someone-else@example.com", time.Hour); err != ErrAdultLimit {
		t.Fatalf("invite into held place = %v", err)
	}
	if _, err := service.CreateInvitation(ctx, owner, "partner@example.com", time.Hour); err != nil {
		t.Fatalf("invite removed partner again: %v", err)
	}
	if access, _ := service.Access(ctx, "home"); access[1].State != "invited" {
		t.Fatalf("re-invited partner state = %q", access[1].State)
	}
}

func seedOwner(t *testing.T, db *sql.DB, now time.Time) policy.ActorScope {
	t.Helper()
	stamp := now.Format(time.RFC3339Nano)
	for _, statement := range []string{
		`INSERT INTO users(id,email,status,password_hash,created_at,updated_at) VALUES('owner','owner@example.com','active','hash',?1,?1)`,
		`INSERT INTO households(id,status,owner_user_id,created_at,updated_at) VALUES('home','active','owner',?1,?1)`,
		`INSERT INTO household_members(household_id,user_id,role,created_at) VALUES('home','owner','owner',?1)`,
	} {
		if _, err := db.Exec(statement, stamp); err != nil {
			t.Fatal(err)
		}
	}
	return policy.ActorScope{ActorID: "owner", HouseholdID: "home", Role: "owner"}
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
