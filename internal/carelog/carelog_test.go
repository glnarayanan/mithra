package carelog

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/policy"
)

var fixedNow = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

func newFixture(t *testing.T) (*Service, policy.ActorScope, policy.ActorScope, policy.ActorScope) {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "mithra.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	stamp := "2026-07-01T00:00:00Z"
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range [][]string{{"owner", "owner@example.com"}, {"partner", "partner@example.com"}, {"outsider", "outside@example.com"}} {
		exec(`INSERT INTO users(id,email,status,password_hash,created_at,updated_at) VALUES(?,?,'active','hash',?,?)`, v[0], v[1], stamp, stamp)
	}
	for _, v := range [][]string{{"home", "owner"}, {"other-home", "outsider"}} {
		exec(`INSERT INTO households(id,status,owner_user_id,created_at,updated_at) VALUES(?,'active',?,?,?)`, v[0], v[1], stamp, stamp)
	}
	for _, v := range [][]string{{"home", "owner", "owner"}, {"home", "partner", "adult"}, {"other-home", "outsider", "owner"}} {
		exec(`INSERT INTO household_members(household_id,user_id,role,created_at) VALUES(?,?,?,?)`, v[0], v[1], v[2], stamp)
	}
	exec(`INSERT INTO household_people(id,household_id,owner_user_id,visibility,display_name,relationship,birth_date,created_at,updated_at) VALUES('baby','home','owner','shared','Meera','child','2026-10-01',?,?)`, stamp, stamp)
	exec(`INSERT INTO household_people(id,household_id,owner_user_id,visibility,display_name,relationship,created_at,updated_at) VALUES('private','home','owner','personal','Aunt','other',?,?)`, stamp, stamp)
	owner := policy.ActorScope{ActorID: "owner", HouseholdID: "home", Role: "owner"}
	partner := policy.ActorScope{ActorID: "partner", HouseholdID: "home", Role: "adult"}
	outsider := policy.ActorScope{ActorID: "outsider", HouseholdID: "other-home", Role: "owner"}
	return New(db).WithClock(func() time.Time { return fixedNow }), owner, partner, outsider
}

func TestValidateShapesEachType(t *testing.T) {
	bad := []Input{
		{Type: "nap"},
		{Type: Feed, Fields: map[string]string{"mode": "spoon"}},
		{Type: Feed, Fields: map[string]string{"mode": "breast"}},
		{Type: Feed, Fields: map[string]string{"mode": "bottle", "offered_ml": "60", "consumed_ml": "90"}},
		{Type: Feed, Fields: map[string]string{"mode": "bottle", "offered_ml": "lots"}},
		{Type: Diaper, Fields: map[string]string{"kind": "damp"}},
		{Type: SpitUp},
		{Type: Weight},
		{Type: Weight, Fields: map[string]string{"kg": "0"}},
		{Type: Milestone},
	}
	for _, in := range bad {
		var inputErr *InputError
		if _, err := Validate(in); !errors.As(err, &inputErr) {
			t.Fatalf("%#v err=%v", in, err)
		}
	}
	bottle, err := Validate(Input{Type: Feed, Fields: map[string]string{"mode": "bottle", "offered_ml": "90", "consumed_ml": "75.0", "side": "left"}})
	if err != nil || bottle["milk_type"] != "formula" || bottle["consumed_ml"] != "75" || bottle["side"] != "" || bottle["running"] != "" {
		t.Fatalf("bottle=%#v %v", bottle, err)
	}
	breast, _ := Validate(Input{Type: Feed, Fields: map[string]string{"mode": "breast", "side": "left"}})
	if breast["running"] != "yes" {
		t.Fatalf("breast without minutes should run: %#v", breast)
	}
	timed, _ := Validate(Input{Type: Feed, Fields: map[string]string{"mode": "breast", "side": "both", "duration_min": "15"}})
	if timed["running"] != "" || timed["duration_min"] != "15" {
		t.Fatalf("timed=%#v", timed)
	}
}

func TestCreateDedupesOnClientIDAndRespectsVisibility(t *testing.T) {
	ctx := context.Background()
	service, owner, partner, outsider := newFixture(t)
	in := Input{Type: Diaper, ClientID: "phone-1", OccurredAt: fixedNow.Add(-time.Hour), Fields: map[string]string{"kind": "wet"}}
	first, err := service.Create(ctx, owner, "baby", in)
	if err != nil || first.ActorEmail != "owner@example.com" || first.Source != "web" {
		t.Fatalf("first=%#v %v", first, err)
	}
	again, err := service.Create(ctx, partner, "baby", in)
	if err != nil || again.ID != first.ID || !again.Duplicate || first.Duplicate {
		t.Fatalf("retry made a new entry: %#v %v", again, err)
	}
	if list, _ := service.List(ctx, partner, "baby", fixedNow.Add(-24*time.Hour)); len(list) != 1 {
		t.Fatalf("partner sees %d", len(list))
	}
	if _, err := service.Create(ctx, outsider, "baby", in); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outsider err=%v", err)
	}
	if _, err := service.Create(ctx, partner, "private", Input{Type: Diaper, Fields: map[string]string{"kind": "wet"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("private person err=%v", err)
	}
	var inputErr *InputError
	if _, err := service.Create(ctx, owner, "baby", Input{Type: Diaper, OccurredAt: fixedNow.Add(time.Hour), Fields: map[string]string{"kind": "wet"}}); !errors.As(err, &inputErr) {
		t.Fatalf("future err=%v", err)
	}
}

func TestRunningFeedFinishDeleteAndRestore(t *testing.T) {
	ctx := context.Background()
	service, owner, partner, _ := newFixture(t)
	feed, err := service.Create(ctx, owner, "baby", Input{Type: Feed, OccurredAt: fixedNow.Add(-20 * time.Minute), Fields: map[string]string{"mode": "breast", "side": "left"}})
	if err != nil || !feed.Running() {
		t.Fatalf("feed=%#v %v", feed, err)
	}
	done, err := service.Finish(ctx, partner, "baby", feed.ID, time.Time{})
	if err != nil || done.Running() || done.Details["duration_min"] != "20" || !done.EndedAt.Equal(fixedNow) {
		t.Fatalf("done=%#v %v", done, err)
	}
	var inputErr *InputError
	if _, err := service.Finish(ctx, owner, "baby", feed.ID, time.Time{}); !errors.As(err, &inputErr) {
		t.Fatalf("second finish err=%v", err)
	}
	if err := service.Delete(ctx, owner, "baby", feed.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := service.List(ctx, owner, "baby", fixedNow.Add(-time.Hour)); len(list) != 0 {
		t.Fatalf("deleted entry still listed: %#v", list)
	}
	if err := service.Restore(ctx, partner, "baby", feed.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := service.List(ctx, owner, "baby", fixedNow.Add(-time.Hour)); len(list) != 1 {
		t.Fatalf("restored entry missing: %#v", list)
	}
	if err := service.Delete(ctx, owner, "baby", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing delete err=%v", err)
	}
}

func TestWeightsOldestFirst(t *testing.T) {
	ctx := context.Background()
	service, owner, _, _ := newFixture(t)
	for i, kg := range []string{"3.9", "3.2", "3.5"} {
		at := fixedNow.Add(-time.Duration(3-i) * 24 * time.Hour)
		if i == 1 {
			at = fixedNow.Add(-10 * 24 * time.Hour)
		}
		if _, err := service.Create(ctx, owner, "baby", Input{Type: Weight, OccurredAt: at, Source: "import", ClientID: "import:" + kg, Fields: map[string]string{"kg": kg}}); err != nil {
			t.Fatal(err)
		}
	}
	weights, err := service.Weights(ctx, owner, "baby")
	if err != nil || len(weights) != 3 || weights[0].Details["kg"] != "3.2" || weights[0].Source != "import" {
		t.Fatalf("weights=%#v %v", weights, err)
	}
}

func TestSummariseCountsSinceMidnightAndMedianGap(t *testing.T) {
	now := fixedNow
	at := func(h, m int) time.Time { return time.Date(2026, 11, 10, h, m, 0, 0, time.UTC) }
	events := []Event{
		{Type: Feed, OccurredAt: at(8, 30), Details: map[string]string{"mode": "breast", "running": "yes"}},
		{Type: Feed, OccurredAt: at(5, 30), EndedAt: at(5, 50), Details: map[string]string{"mode": "breast"}},
		{Type: Feed, OccurredAt: at(2, 0), Details: map[string]string{"mode": "bottle"}},
		{Type: Feed, OccurredAt: at(2, 0).Add(-3 * time.Hour), Details: map[string]string{"mode": "bottle"}},
		{Type: Diaper, OccurredAt: at(6, 0), Details: map[string]string{"kind": "both"}},
		{Type: Diaper, OccurredAt: at(3, 0), Details: map[string]string{"kind": "wet"}},
		{Type: Diaper, OccurredAt: at(2, 0).Add(-4 * time.Hour), Details: map[string]string{"kind": "dirty"}},
		{Type: SpitUp, OccurredAt: at(7, 0), Details: map[string]string{"size": "small"}},
	}
	today := Summarise(events, now)
	if today.Feeds != 3 || today.Wet != 2 || today.Dirty != 1 || today.SpitUps != 1 {
		t.Fatalf("counts=%#v", today)
	}
	if today.LastFeed == nil || !today.LastFeed.OccurredAt.Equal(at(8, 30)) || today.Running == nil {
		t.Fatalf("last=%#v running=%#v", today.LastFeed, today.Running)
	}
	// Gaps: 3h, 3h30, 3h. Median 3h.
	if today.MedianGap != 3*time.Hour || today.GapSamples != 3 {
		t.Fatalf("median=%v samples=%d", today.MedianGap, today.GapSamples)
	}
	if empty := Summarise(nil, now); empty.LastFeed != nil || empty.GapSamples != 0 {
		t.Fatalf("empty=%#v", empty)
	}
}
