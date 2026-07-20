package health

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/storage"
)

type healthFixture struct {
	db      *sql.DB
	service *Service
	sources *storage.Service
	owner   policy.ActorScope
	partner policy.ActorScope
}

func newHealthFixture(t *testing.T) healthFixture {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "mithra.sqlite3"))
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
	master := make([]byte, 32)
	for i := range master {
		master[i] = byte(i + 1)
	}
	sources, err := storage.New(db, filepath.Join(t.TempDir(), "sources"), master)
	if err != nil {
		t.Fatal(err)
	}
	return healthFixture{db: db, service: New(db), sources: sources, owner: policy.ActorScope{ActorID: "owner", HouseholdID: "home", Role: "owner"}, partner: policy.ActorScope{ActorID: "partner", HouseholdID: "home", Role: "adult"}}
}

func (f healthFixture) source(t *testing.T, visibility policy.Visibility, content []byte) storage.Source {
	t.Helper()
	s, err := f.sources.Store(context.Background(), f.owner, content, storage.Metadata{Family: "csv", Version: 1, Visibility: visibility, LocatorKind: "source", LocatorValue: "health.csv"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGoldenHealthFixturePreservesContextAndRefusesUnknownConversion(t *testing.T) {
	f := newHealthFixture(t)
	content, err := os.ReadFile(filepath.Join("..", "..", "testdata", "imports", "health", "observations.csv"))
	if err != nil {
		t.Fatal(err)
	}
	source := f.source(t, policy.Shared, content)
	rows, err := csv.NewReader(bytes.NewReader(content)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var potassiumID string
	for i, row := range rows[1:] {
		o, err := f.service.CreateObservation(context.Background(), f.owner, ObservationDraft{Visibility: policy.Visibility(row[11]), Subject: row[0], Analyte: row[1], Specimen: row[2], Method: row[3], ReferenceContext: row[4], ObservedOn: row[5], Value: row[6], Unit: row[7], ReferenceLow: row[8], ReferenceHigh: row[9], ReferenceUnit: row[10], Provenance: Provenance{SourceID: source.ID, SourceFamily: "csv", SourceVersion: 1, LocatorKind: "row", LocatorValue: string(rune('2' + i)), GeneratedBy: "application", SchemaVersion: "health-v1"}})
		if err != nil {
			t.Fatalf("row %d: %v", i+2, err)
		}
		if row[1] == "Potassium" && row[7] == "mg/dL" {
			potassiumID = o.ID
		}
	}
	summary, err := f.service.Summarize(context.Background(), f.owner, AllRecords)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Observations) != 5 || len(summary.Series) != 2 || len(summary.Conflicts) != 2 {
		t.Fatalf("summary counts observations=%d series=%d conflicts=%d", len(summary.Observations), len(summary.Series), len(summary.Conflicts))
	}
	var serum *Series
	for i := range summary.Series {
		if summary.Series[i].Analyte == "Glucose" && summary.Series[i].Specimen == "serum" {
			serum = &summary.Series[i]
		}
	}
	if serum == nil || len(serum.Observations) != 2 || serum.Unit != "mg/dL" || serum.Observations[0].Value.PlainString() != "100.00" || serum.Observations[1].Value.PlainString() != "105.0" {
		t.Fatalf("serum series = %#v", serum)
	}
	var current Observation
	for _, o := range summary.Observations {
		if o.ID == potassiumID {
			current = o
		}
	}
	corrected, err := f.service.CorrectObservation(context.Background(), f.owner, current.ID, current.Version, "4.3", "mmol/L")
	if err != nil || corrected.GeneratedBy != "user" || corrected.SupersedesID != current.ID {
		t.Fatalf("correction = %#v, %v", corrected, err)
	}
	after, err := f.service.Summarize(context.Background(), f.owner, AllRecords)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Conflicts) != 0 || len(after.Series) != 3 {
		t.Fatalf("corrected summary series=%d conflicts=%d", len(after.Series), len(after.Conflicts))
	}
}

func TestHealthPrivacyDatesAndSourceScope(t *testing.T) {
	f := newHealthFixture(t)
	shared := f.source(t, policy.Shared, []byte("shared report"))
	personal := f.source(t, policy.Personal, []byte("private report"))
	prov := func(s storage.Source) Provenance {
		return Provenance{SourceID: s.ID, SourceFamily: s.Family, SourceVersion: s.Version, LocatorKind: "source", LocatorValue: s.LocatorValue}
	}
	if _, err := f.service.CreateObservation(context.Background(), f.owner, ObservationDraft{Visibility: policy.Shared, Subject: "Alex", Analyte: "Weight", ObservedOn: "2026-07-01", Value: "72.5", Unit: "kg", Provenance: prov(shared)}); err != nil {
		t.Fatal(err)
	}
	private, err := f.service.CreateObservation(context.Background(), f.owner, ObservationDraft{Visibility: policy.Personal, Subject: "Alex", Analyte: "Weight", ObservedOn: "2026-07-02", Value: "72.0", Unit: "kg", Provenance: prov(personal)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CreateAppointment(context.Background(), f.owner, AppointmentDraft{Visibility: policy.Shared, Subject: "Alex", Label: "Annual check-up", ScheduledOn: "2026-07-25", Provenance: prov(shared)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CreateRoutine(context.Background(), f.owner, RoutineDraft{Visibility: policy.Shared, Subject: "Alex", Label: "Recorded routine", Cadence: "Every morning", NextDueOn: "2026-07-19", Provenance: prov(shared)}); err != nil {
		t.Fatal(err)
	}
	partner, err := f.service.Summarize(context.Background(), f.partner, AllRecords)
	if err != nil || len(partner.Observations) != 1 || len(partner.Appointments) != 1 || len(partner.Routines) != 1 {
		t.Fatalf("partner summary=%#v, %v", partner, err)
	}
	ownerShared, err := f.service.Summarize(context.Background(), f.owner, SharedRecords)
	if err != nil || len(ownerShared.Observations) != 1 {
		t.Fatalf("shared summary=%#v, %v", ownerShared, err)
	}
	if _, err := f.service.CorrectObservation(context.Background(), f.partner, private.ID, private.Version, "70", "kg"); !errors.Is(err, policy.ErrUnauthorized) {
		t.Fatalf("private correction error=%v", err)
	}
	if _, err := f.service.CreateObservation(context.Background(), f.owner, ObservationDraft{Visibility: policy.Shared, Subject: "Alex", Analyte: "Private leak", ObservedOn: "2026-07-01", Value: "1", Unit: "IU/L", Provenance: prov(personal)}); err == nil {
		t.Fatal("shared observation cited personal source")
	}
	if _, err := f.db.Exec(`UPDATE users SET status='disabled',disabled_at=updated_at WHERE id='owner'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Summarize(context.Background(), f.owner, AllRecords); !errors.Is(err, policy.ErrUnauthorized) {
		t.Fatalf("disabled summary error=%v", err)
	}
}

func TestAppointmentTimeMustMatchItsDate(t *testing.T) {
	f := newHealthFixture(t)
	source := f.source(t, policy.Personal, []byte("appointment"))
	draft := AppointmentDraft{Visibility: policy.Personal, Subject: "Owner", Label: "Check-up", ScheduledOn: "2026-07-20", ScheduledAt: "2026-07-21T10:00", Provenance: Provenance{SourceID: source.ID, SourceFamily: source.Family, SourceVersion: source.Version, LocatorKind: "source", LocatorValue: source.LocatorValue}}
	if _, err := f.service.CreateAppointment(context.Background(), f.owner, draft); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("cross-date appointment error=%v", err)
	}
	draft.ScheduledAt = "2026-07-20 10:00"
	if _, err := f.service.CreateAppointment(context.Background(), f.owner, draft); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("malformed appointment error=%v", err)
	}
	draft.ScheduledAt = "2026-07-20T10:00"
	appointment, err := f.service.CreateAppointment(context.Background(), f.owner, draft)
	if err != nil || appointment.ScheduledAt != draft.ScheduledAt {
		t.Fatalf("valid appointment=%#v error=%v", appointment, err)
	}
}
