package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/policy"
)

func TestEnqueueDeduplicatesAndCompletePublishesOnce(t *testing.T) {
	service, db, scope, _ := jobsFixture(t)
	spec := Spec{Kind: "finance", SubjectID: "record:7", Visibility: policy.Shared, IdempotencyKey: "finance-record-7-v1", MaxAttempts: 3}
	first, err := service.Enqueue(context.Background(), scope, spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Enqueue(context.Background(), scope, spec)
	if err != nil || second.ID != first.ID {
		t.Fatalf("dedupe = %#v, %v; want %s", second, err, first.ID)
	}
	var storedKey string
	if err := db.QueryRow(`SELECT idempotency_hash FROM jobs WHERE id=?`, first.ID).Scan(&storedKey); err != nil || storedKey == spec.IdempotencyKey || len(storedKey) != 64 {
		t.Fatalf("stored idempotency = %q, %v", storedKey, err)
	}
	if _, err := db.Exec(`CREATE TABLE published(id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	lease, err := service.Claim(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Job.ID != first.ID || lease.Job.Attempts != 1 || lease.Job.Generation != 1 || lease.Token == "" {
		t.Fatalf("lease = %#v", lease)
	}
	if err := service.Complete(context.Background(), lease, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO published(id) VALUES(?)`, lease.Job.SubjectID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.Complete(context.Background(), lease, func(*sql.Tx) error { t.Fatal("published twice"); return nil }); !errors.Is(err, ErrLease) {
		t.Fatalf("reused completion = %v", err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM jobs WHERE id=?`, first.ID).Scan(&state); err != nil || state != "succeeded" {
		t.Fatalf("state = %q, %v", state, err)
	}
}

func TestExpiredLeaseIsReclaimedAndStaleWorkerRejected(t *testing.T) {
	service, _, scope, clock := jobsFixture(t)
	job, err := service.Enqueue(context.Background(), scope, Spec{Kind: "coaching", SubjectID: "brief:week", IdempotencyKey: "coaching-week-2026-29", MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Claim(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(2 * time.Minute)
	second, err := service.Claim(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.Job.ID != job.ID || second.Job.Generation != first.Job.Generation+1 || second.Job.Attempts != 2 {
		t.Fatalf("reclaimed lease = %#v after %#v", second, first)
	}
	if err := service.Complete(context.Background(), first, func(*sql.Tx) error { t.Fatal("stale worker published"); return nil }); !errors.Is(err, ErrLease) {
		t.Fatalf("stale completion = %v", err)
	}
	if err := service.Complete(context.Background(), second, func(*sql.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestFailureRetriesAreBounded(t *testing.T) {
	service, db, scope, clock := jobsFixture(t)
	job, err := service.Enqueue(context.Background(), scope, Spec{Kind: "extract", SubjectID: "source:1", IdempotencyKey: "extract-source-1-v1", MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Claim(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Fail(context.Background(), first, "provider_timeout", time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Claim(context.Background(), time.Minute); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("early retry claim = %v", err)
	}
	*clock = clock.Add(2 * time.Second)
	second, err := service.Claim(context.Background(), time.Minute)
	if err != nil || second.Job.Attempts != 2 {
		t.Fatalf("second claim = %#v, %v", second, err)
	}
	if err := service.Fail(context.Background(), second, "provider_timeout", time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Claim(context.Background(), time.Minute); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("exhausted claim = %v", err)
	}
	var state, code string
	if err := db.QueryRow(`SELECT state,last_error_code FROM jobs WHERE id=?`, job.ID).Scan(&state, &code); err != nil || state != "failed" || code != "provider_timeout" {
		t.Fatalf("terminal state = %q %q, %v", state, code, err)
	}
}

func TestAuthorizeCancelsStaleLeaseBeforeProviderDispatch(t *testing.T) {
	service, db, scope, clock := jobsFixture(t)
	job, err := service.Enqueue(context.Background(), scope, Spec{Kind: "coaching", SubjectID: "brief:week", IdempotencyKey: "predispatch-coaching-week", MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := service.Claim(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	stamp := clock.Add(time.Second).UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`UPDATE users SET status='disabled',disabled_at=?,updated_at=? WHERE id=?`, stamp, stamp, scope.ActorID); err != nil {
		t.Fatal(err)
	}
	if err := service.Authorize(context.Background(), lease); !errors.Is(err, ErrStale) {
		t.Fatalf("pre-dispatch authorization = %v", err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM jobs WHERE id=?`, job.ID).Scan(&state); err != nil || state != "cancelled" {
		t.Fatalf("stale pre-dispatch state = %q, %v", state, err)
	}
}

func TestCompletionFencesSourceRevisionAndMembershipChanges(t *testing.T) {
	for _, change := range []string{"source deleted", "visibility changed", "member disabled", "member removed", "shared revision"} {
		t.Run(change, func(t *testing.T) {
			service, db, scope, clock := jobsFixture(t)
			sourceID := insertSource(t, db, scope, policy.Personal, "A")
			visibility := policy.Personal
			if change == "shared revision" {
				visibility = policy.Personal
			}
			job, err := service.Enqueue(context.Background(), scope, Spec{Kind: "health", SubjectID: "observation:1", SourceID: sourceID, Visibility: visibility, IdempotencyKey: "health-observation-1", MaxAttempts: 2})
			if err != nil {
				t.Fatal(err)
			}
			lease, err := service.Claim(context.Background(), time.Minute)
			if err != nil || lease.Job.ID != job.ID {
				t.Fatalf("claim = %#v, %v", lease, err)
			}
			stamp := clock.Add(time.Second).UTC().Format(time.RFC3339Nano)
			switch change {
			case "source deleted":
				_, err = db.Exec(`UPDATE sources SET state='deleted',updated_at=? WHERE id=?`, stamp, sourceID)
			case "visibility changed":
				_, err = db.Exec(`UPDATE sources SET visibility='shared',updated_at=? WHERE id=?`, stamp, sourceID)
			case "member disabled":
				_, err = db.Exec(`UPDATE users SET status='disabled',disabled_at=?,updated_at=? WHERE id=?`, stamp, stamp, scope.ActorID)
			case "member removed":
				_, err = db.Exec(`DELETE FROM household_members WHERE user_id=?`, scope.ActorID)
			case "shared revision":
				insertSource(t, db, scope, policy.Shared, "B")
			}
			if err != nil {
				t.Fatal(err)
			}
			published := false
			if err := service.Complete(context.Background(), lease, func(*sql.Tx) error { published = true; return nil }); !errors.Is(err, ErrStale) {
				t.Fatalf("completion = %v", err)
			}
			if published {
				t.Fatal("stale job published")
			}
			var state string
			if err := db.QueryRow(`SELECT state FROM jobs WHERE id=?`, job.ID).Scan(&state); err != nil || state != "cancelled" {
				t.Fatalf("cancelled state = %q, %v", state, err)
			}
		})
	}
}

func jobsFixture(t *testing.T) (*Service, *sql.DB, policy.ActorScope, *time.Time) {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "mithra.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	stamp := clock.Format(time.RFC3339Nano)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,email,status,password_hash,created_at,updated_at) VALUES('owner','owner@example.com','active','hash',?,?)`, []any{stamp, stamp}},
		{`INSERT INTO households(id,status,owner_user_id,created_at,updated_at) VALUES('home','active','owner',?,?)`, []any{stamp, stamp}},
		{`INSERT INTO household_members(household_id,user_id,role,created_at) VALUES('home','owner','owner',?)`, []any{stamp}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	next := 0
	service := New(db)
	service.now = func() time.Time { return clock }
	service.token = func() (string, error) {
		next++
		return fmt.Sprintf("job-token-%03d-with-enough-entropy", next), nil
	}
	return service, db, policy.ActorScope{ActorID: "owner", HouseholdID: "home", Role: "owner"}, &clock
}

func insertSource(t *testing.T, db *sql.DB, scope policy.ActorScope, visibility policy.Visibility, keyCharacter string) string {
	t.Helper()
	id := fmt.Sprintf("source-%s-%d", keyCharacter, time.Now().UnixNano())
	key := strings.Repeat(keyCharacter, 43)
	digest := strings.Repeat("a", 64)
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(`INSERT INTO sources(id,household_id,owner_user_id,visibility,family,source_version,state,storage_key,plaintext_size,plaintext_digest,locator_kind,locator_value,created_at,updated_at) VALUES(?,?,?,?,? ,1,'live',?,5,?,'source','fixture',?,?)`, id, scope.HouseholdID, scope.ActorID, visibility, "text", key, digest, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
