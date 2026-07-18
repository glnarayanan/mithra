package planning

import (
	"context"
	"encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/storage"
)

func TestEventsKeepCalendarViewsPrivateAndDetectOwnerConflict(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "mithra.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stamp := "2026-01-01T00:00:00Z"
	for _, x := range [][]string{{"owner", "owner@example.com"}, {"partner", "partner@example.com"}} {
		if _, err = db.Exec(`INSERT INTO users(id,email,status,password_hash,created_at,updated_at) VALUES(?,?,'active','x',?,?)`, x[0], x[1], stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO households(id,status,owner_user_id,created_at,updated_at) VALUES('home','active','owner',?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	for _, x := range [][]string{{"owner", "owner"}, {"partner", "adult"}} {
		if _, err = db.Exec(`INSERT INTO household_members(household_id,user_id,role,created_at) VALUES('home',?,?,?)`, x[0], x[1], stamp); err != nil {
			t.Fatal(err)
		}
	}
	key := make([]byte, 32)
	store, err := storage.New(db, filepath.Join(t.TempDir(), "sources"), key)
	if err != nil {
		t.Fatal(err)
	}
	owner := policy.ActorScope{ActorID: "owner", HouseholdID: "home", Role: "owner"}
	partner := policy.ActorScope{ActorID: "partner", HouseholdID: "home", Role: "adult"}
	source, err := store.Store(ctx, owner, []byte("calendar"), storage.Metadata{Family: "csv", Version: 1, Visibility: policy.Shared, LocatorKind: "row", LocatorValue: "1"})
	if err != nil {
		t.Fatal(err)
	}
	s := New(db)
	p := Provenance{SourceID: source.ID, SourceFamily: "csv", SourceVersion: 1, LocatorKind: "row", LocatorValue: "1"}
	completed, err := s.CreateEvent(ctx, owner, EventDraft{Visibility: policy.Shared, Title: "Already complete", AllDay: true, StartsOn: "2028-02-28", Status: "completed", Provenance: p})
	if err != nil || completed.Status != "completed" {
		t.Fatalf("completed import=%#v err=%v", completed, err)
	}
	if eligible, err := s.Events(ctx, partner, AllRecords, "2028-02-28", "2028-02-28"); err != nil || len(eligible) != 0 {
		t.Fatalf("completed eligibility=%#v err=%v", eligible, err)
	}
	first, err := s.CreateEvent(ctx, owner, EventDraft{Visibility: policy.Shared, Title: "Travel", AllDay: true, StartsOn: "2028-02-29", OwnerIDs: []string{"owner"}, Provenance: p})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateEvent(ctx, owner, EventDraft{Visibility: policy.Shared, Title: "Overlap", AllDay: false, StartsAt: "2028-02-29T09:00", EndsAt: "2028-02-29T10:00", Timezone: "Asia/Kolkata", OwnerIDs: []string{"owner"}, Provenance: p})
	if err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(ctx, partner, AllRecords, "2028-02-01", "2028-02-29")
	if err != nil || len(events) != 2 || events[0].LocatorKind != "row" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	conflicts, err := s.Conflicts(ctx, owner, AllRecords, "2028-02-29", "2028-02-29")
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("conflicts=%#v err=%v", conflicts, err)
	}
	privateSource, err := store.Store(ctx, owner, []byte("private"), storage.Metadata{Family: "csv", Version: 1, Visibility: policy.Personal, LocatorKind: "row", LocatorValue: "2"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateEvent(ctx, owner, EventDraft{Title: "Private", AllDay: true, StartsOn: "2028-02-29", Provenance: Provenance{SourceID: privateSource.ID, SourceFamily: "csv", SourceVersion: 1, LocatorKind: "row", LocatorValue: "2"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Events(ctx, partner, AllRecords, "2028-02-29", "2028-02-29"); err != nil || len(got) != 2 {
		t.Fatalf("partner=%#v err=%v", got, err)
	}
	if _, err := s.GetEvent(ctx, partner, first.ID); err != nil {
		t.Fatal(err)
	}
	private, err := s.Events(ctx, owner, PersonalRecords, "2028-02-29", "2028-02-29")
	if err != nil || len(private) != 1 {
		t.Fatalf("private=%#v err=%v", private, err)
	}
	if _, err := s.GetEvent(ctx, partner, private[0].ID); !errors.Is(err, policy.ErrUnauthorized) {
		t.Fatalf("private get=%v", err)
	}
	if err := s.SetTimezone(ctx, owner, "Asia/Kolkata"); err != nil {
		t.Fatal(err)
	}
	if z, err := s.GetTimezone(ctx, partner); err != nil || z != "Asia/Kolkata" {
		t.Fatalf("zone=%q err=%v", z, err)
	}
	if err := s.SetTimezone(ctx, partner, "UTC"); !errors.Is(err, policy.ErrUnauthorized) {
		t.Fatalf("partner timezone=%v", err)
	}
	if _, err := s.CreateEvent(ctx, owner, EventDraft{Visibility: policy.Shared, Title: "Backwards", AllDay: true, StartsOn: "2028-03-02", EndsOn: "2028-03-01", Provenance: p}); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("backwards event=%v", err)
	}
	fixture, err := os.Open(filepath.Join("..", "..", "testdata", "imports", "planning", "events.csv"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(fixture).ReadAll()
	fixture.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows[1:] {
		allDay, err := strconv.ParseBool(row[1])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateEvent(ctx, owner, EventDraft{Visibility: policy.Shared, Title: row[0], AllDay: allDay, StartsOn: row[2], EndsOn: row[3], StartsAt: row[4], EndsAt: row[5], Timezone: row[6], Provenance: p}); err != nil {
			t.Fatalf("golden row %q: %v", row[0], err)
		}
	}
	if leap, err := s.Events(ctx, partner, AllRecords, "2028-02-29", "2028-02-29"); err != nil || len(leap) != 3 {
		t.Fatalf("golden leap=%#v err=%v", leap, err)
	}
	if dst, err := s.Events(ctx, partner, AllRecords, "2026-03-08", "2026-03-08"); err != nil || len(dst) != 1 || dst[0].Timezone != "America/New_York" {
		t.Fatalf("golden dst=%#v err=%v", dst, err)
	}
	if _, err := db.Exec(`UPDATE users SET status='disabled',disabled_at=updated_at WHERE id='owner'`); err != nil {
		t.Fatal(err)
	}
	remaining, err := s.Events(ctx, partner, AllRecords, "2028-02-29", "2028-02-29")
	if err != nil || len(remaining) != 3 || len(remaining[0].OwnerIDs) != 0 || len(remaining[1].OwnerIDs) != 0 || len(remaining[2].OwnerIDs) != 0 {
		t.Fatalf("disabled owner eligibility=%#v err=%v", remaining, err)
	}
}
