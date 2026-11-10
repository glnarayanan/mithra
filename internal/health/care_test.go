package health

import (
	"context"
	"errors"
	"testing"

	"github.com/glnarayanan/mithra/internal/policy"
)

func TestCareLogKeepsPrivateEntriesPrivate(t *testing.T) {
	ctx := context.Background()
	f := newHealthFixture(t)
	medicine, err := f.service.CreateMedicine(ctx, f.owner, MedicineDraft{Subject: "Meera", Name: "Vitamin D drops", Dosage: "400 IU daily", StartedOn: "2026-03-20", Visibility: policy.Shared})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CreateNote(ctx, f.owner, NoteDraft{Subject: "Meera", RecordedOn: "2026-11-02", Content: "Paediatrician said weight is on her curve."}); err != nil {
		t.Fatal(err)
	}
	habit, err := f.service.CreateHabit(ctx, f.partner, HabitDraft{Subject: "Meera", Name: "Tummy time", Kind: "number", Unit: "min", Visibility: policy.Shared})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.LogHabit(ctx, f.owner, habit.ID, "2026-11-10", "15"); err != nil {
		t.Fatal(err)
	}
	if err := f.service.LogHabit(ctx, f.owner, habit.ID, "2026-11-10", "20"); err != nil {
		t.Fatal(err)
	}
	if err := f.service.LogHabit(ctx, f.owner, habit.ID, "2026-11-10", "lots"); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("non-number log err=%v", err)
	}
	owner, err := f.service.CareLogFor(ctx, f.owner, "Meera", "2026-11-04")
	if err != nil || len(owner.Medicines) != 1 || len(owner.Notes) != 1 || len(owner.Habits) != 1 || owner.Habits[0].Logs["2026-11-10"] != "20" {
		t.Fatalf("owner log=%#v %v", owner, err)
	}
	partner, _ := f.service.CareLogFor(ctx, f.partner, "Meera", "2026-11-04")
	if len(partner.Notes) != 0 || len(partner.Medicines) != 1 {
		t.Fatalf("personal note leaked to partner: %#v", partner)
	}
	if err := f.service.EndMedicine(ctx, f.partner, medicine.ID, 1, "2026-11-01"); err != nil {
		t.Fatal(err)
	}
	if err := f.service.EndMedicine(ctx, f.partner, medicine.ID, 1, "2026-11-01"); !errors.Is(err, policy.ErrConflict) {
		t.Fatalf("second end err=%v", err)
	}
	if _, err := f.service.CreateMedicine(ctx, f.owner, MedicineDraft{Subject: "", Name: "x"}); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("missing subject err=%v", err)
	}
	if err := f.service.StopHabit(ctx, f.owner, habit.ID); err != nil {
		t.Fatal(err)
	}
	if stopped, _ := f.service.CareLogFor(ctx, f.owner, "", "2026-11-04"); len(stopped.Habits) != 0 {
		t.Fatalf("stopped habit still listed")
	}
}

func TestCatalogFindsSynonymsAndCategories(t *testing.T) {
	if CanonicalAnalyte("fbs") != "Fasting Glucose" || CategoryFor("A1C") != "Diabetes" || CanonicalAnalyte("Body weight") != "Weight" || CategoryFor("Unknown thing") != "Other" {
		t.Fatalf("catalog lookups failed")
	}
	if tests := Catalog(); len(tests) < 60 || tests[0].Category != "Body" {
		t.Fatalf("catalog order=%v", tests[0])
	}
}
