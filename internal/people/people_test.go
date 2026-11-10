package people

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/policy"
)

func newFixture(t *testing.T) (*Service, policy.ActorScope, policy.ActorScope, policy.ActorScope) {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "mithra.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	stamp := "2026-07-01T00:00:00Z"
	for _, v := range [][]string{{"owner", "owner@example.com"}, {"partner", "partner@example.com"}, {"outsider", "outside@example.com"}} {
		if _, err := db.Exec(`INSERT INTO users(id,email,status,password_hash,created_at,updated_at) VALUES(?,?,'active','hash',?,?)`, v[0], v[1], stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range [][]string{{"home", "owner"}, {"other-home", "outsider"}} {
		if _, err := db.Exec(`INSERT INTO households(id,status,owner_user_id,created_at,updated_at) VALUES(?,'active',?,?,?)`, v[0], v[1], stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range [][]string{{"home", "owner", "owner"}, {"home", "partner", "adult"}, {"other-home", "outsider", "owner"}} {
		if _, err := db.Exec(`INSERT INTO household_members(household_id,user_id,role,created_at) VALUES(?,?,?,?)`, v[0], v[1], v[2], stamp); err != nil {
			t.Fatal(err)
		}
	}
	owner := policy.ActorScope{ActorID: "owner", HouseholdID: "home", Role: "owner"}
	partner := policy.ActorScope{ActorID: "partner", HouseholdID: "home", Role: "adult"}
	outsider := policy.ActorScope{ActorID: "outsider", HouseholdID: "other-home", Role: "owner"}
	return New(db), owner, partner, outsider
}

func TestPeopleSharedByDefaultPrivateStaysPrivate(t *testing.T) {
	ctx := context.Background()
	service, owner, partner, outsider := newFixture(t)
	child, err := service.Create(ctx, owner, Draft{Name: "  Meera  ", Relationship: "Child", BirthDate: "2026-03-14"})
	if err != nil || child.Visibility != policy.Shared || child.Name != "Meera" || child.Relationship != "child" {
		t.Fatalf("create=%#v %v", child, err)
	}
	if _, err := service.Create(ctx, owner, Draft{Name: "Private aunt", Relationship: "other", Visibility: policy.Personal}); err != nil {
		t.Fatal(err)
	}
	if list, _ := service.List(ctx, owner); len(list) != 2 {
		t.Fatalf("owner sees %d", len(list))
	}
	if list, _ := service.List(ctx, partner); len(list) != 1 || list[0].Name != "Meera" {
		t.Fatalf("partner sees %#v", list)
	}
	if list, _ := service.List(ctx, outsider); len(list) != 0 {
		t.Fatalf("outsider sees %#v", list)
	}
	if _, err := service.Get(ctx, outsider, child.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outsider get err=%v", err)
	}
}

func TestPeopleUpdateArchiveAndValidation(t *testing.T) {
	ctx := context.Background()
	service, owner, partner, _ := newFixture(t)
	for _, bad := range []Draft{{Name: "", Relationship: "child"}, {Name: "A", Relationship: "pet"}, {Name: "A", Relationship: "child", BirthDate: "14/03/2026"}} {
		if _, err := service.Create(ctx, owner, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("draft %#v err=%v", bad, err)
		}
	}
	child, _ := service.Create(ctx, owner, Draft{Name: "Meera", Relationship: "child"})
	updated, err := service.Update(ctx, partner, child.ID, 1, Draft{Name: "Meera R", Relationship: "child", BirthDate: "2026-03-14"})
	if err != nil || updated.Version != 2 || updated.BirthDate != "2026-03-14" {
		t.Fatalf("update=%#v %v", updated, err)
	}
	if _, err := service.Update(ctx, owner, child.ID, 1, Draft{Name: "Stale", Relationship: "child"}); !errors.Is(err, policy.ErrConflict) {
		t.Fatalf("stale update err=%v", err)
	}
	if err := service.Archive(ctx, owner, child.ID, 2); err != nil {
		t.Fatal(err)
	}
	if list, _ := service.List(ctx, owner); len(list) != 0 {
		t.Fatalf("archived person still listed: %#v", list)
	}
}

func TestAge(t *testing.T) {
	now := time.Date(2026, time.November, 10, 9, 0, 0, 0, time.UTC)
	for birth, want := range map[string]string{"2026-11-05": "5 days", "2026-11-09": "1 day", "2026-10-01": "5 weeks", "2026-03-14": "7 months", "2023-11-10": "3 years", "2024-12-01": "23 months", "2027-01-01": "", "": ""} {
		if got := Age(birth, now); got != want {
			t.Fatalf("Age(%q)=%q want %q", birth, got, want)
		}
	}
}
