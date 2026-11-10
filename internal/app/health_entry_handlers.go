package app

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/glnarayanan/mithra/internal/health"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/storage"
)

// HealthCatalogOption feeds the measurement name list.
type HealthCatalogOption struct{ Name, Unit, Category string }

func healthCatalogOptions() []HealthCatalogOption {
	tests := health.Catalog()
	options := make([]HealthCatalogOption, 0, len(tests))
	for _, test := range tests {
		options = append(options, HealthCatalogOption{Name: test.Name, Unit: test.DefaultUnit, Category: test.Category})
	}
	return options
}

// healthReturnPath keeps redirects on the Health page or a person's page.
func healthReturnPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "/health" || (strings.HasPrefix(value, "/family/") && !strings.ContainsAny(value[len("/family/"):], "/?#\\") && len(value) < 80) {
		return value
	}
	return "/health"
}

func withQuery(path, key, value string) string {
	return path + "?" + url.Values{key: {value}}.Encode()
}

func formVisibility(r *http.Request) policy.Visibility {
	if r.PostForm.Get("visibility") == string(policy.Shared) {
		return policy.Shared
	}
	return policy.Personal
}

// healthEntry handles everything typed directly on Health and person pages:
// measurements, medicines, notes and habits.
func (a *App) healthEntry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowedFor(w, "POST")
		return
	}
	scope, _, ok := a.authenticated(r)
	if !ok {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	if !a.validSessionMutation(r, a.sessionCookie(r)) {
		writeError(w, http.StatusForbidden, "request verification failed")
		return
	}
	back := healthReturnPath(r.PostForm.Get("return"))
	ctx := r.Context()
	subject := boundedField(r, "subject", 128)
	today := a.householdNow(ctx, scope).Format("2006-01-02")
	var err error
	done := "saved"
	switch r.PostForm.Get("action") {
	case "measurement":
		err = a.addMeasurement(r, scope, subject, today)
		done = "measurement"
	case "medicine_add":
		_, err = a.healthRecords.CreateMedicine(ctx, scope, health.MedicineDraft{Subject: subject, Name: boundedField(r, "name", 128), Dosage: boundedField(r, "dosage", 128), StartedOn: boundedField(r, "started_on", 10), Note: boundedField(r, "note", 512), Visibility: formVisibility(r)})
	case "medicine_end":
		version, parseErr := strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
		if parseErr != nil {
			err = health.ErrInvalidRecord
			break
		}
		ended := boundedField(r, "ended_on", 10)
		if ended == "" {
			ended = today
		}
		err = a.healthRecords.EndMedicine(ctx, scope, boundedField(r, "medicine_id", 64), version, ended)
	case "note_add":
		recorded := boundedField(r, "recorded_on", 10)
		if recorded == "" {
			recorded = today
		}
		_, err = a.healthRecords.CreateNote(ctx, scope, health.NoteDraft{Subject: subject, RecordedOn: recorded, Content: boundedField(r, "content", 4000), Visibility: formVisibility(r)})
	case "habit_add":
		_, err = a.healthRecords.CreateHabit(ctx, scope, health.HabitDraft{Subject: subject, Name: boundedField(r, "name", 128), Kind: boundedField(r, "kind", 16), Unit: boundedField(r, "unit", 32), Visibility: formVisibility(r)})
	case "habit_log":
		day := boundedField(r, "day", 10)
		if day == "" {
			day = today
		}
		err = a.healthRecords.LogHabit(ctx, scope, boundedField(r, "habit_id", 64), day, boundedField(r, "value", 64))
	case "habit_stop":
		err = a.healthRecords.StopHabit(ctx, scope, boundedField(r, "habit_id", 64))
	default:
		err = health.ErrInvalidRecord
	}
	if err != nil {
		http.Redirect(w, r, withQuery(back, "entry", "failed"), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, withQuery(back, "entry", done), http.StatusSeeOther)
}

// addMeasurement keeps a typed measurement with the same provenance as any
// other record: the typed words are stored as an encrypted text source.
func (a *App) addMeasurement(r *http.Request, scope policy.ActorScope, subject, today string) error {
	ctx := r.Context()
	analyte := health.CanonicalAnalyte(boundedField(r, "analyte", 128))
	value := boundedField(r, "value", 64)
	unit := boundedField(r, "unit", 64)
	if unit == "" {
		if test, ok := health.LookupTest(analyte); ok {
			unit = test.DefaultUnit
		}
	}
	observed := boundedField(r, "observed_on", 10)
	if observed == "" {
		observed = today
	}
	if _, err := time.Parse("2006-01-02", observed); err != nil || observed > today || subject == "" || analyte == "" || value == "" || unit == "" {
		return health.ErrInvalidRecord
	}
	if _, err := health.ParseValue(value); err != nil {
		return health.ErrInvalidRecord
	}
	visibility := formVisibility(r)
	text := "Measurement entered in Mithra: " + analyte + " for " + subject + " on " + observed + " was " + value + " " + unit + "."
	source, err := a.sources.Store(ctx, scope, []byte(text), storage.Metadata{Family: "text", Version: 1, Visibility: visibility, LocatorKind: "source", LocatorValue: "health-measurement-entry"})
	if err != nil {
		return err
	}
	_, err = a.healthRecords.CreateObservation(ctx, scope, health.ObservationDraft{Visibility: visibility, Subject: subject, Analyte: analyte, ObservedOn: observed, Value: value, Unit: unit, Provenance: health.Provenance{SourceID: source.ID, SourceFamily: source.Family, SourceVersion: source.Version, LocatorKind: "source", LocatorValue: source.ID, GeneratedBy: "user", SchemaVersion: "direct-entry-v1"}})
	if err != nil {
		_ = a.sources.Delete(ctx, scope, source.ID)
		return err
	}
	return nil
}

func healthEntryStatus(value string) string {
	switch value {
	case "measurement":
		return "Measurement added. Its typed words are kept as the source."
	case "saved":
		return "Saved."
	case "failed":
		return "That entry was not saved. Check the name, the number and the date, then try again."
	}
	return ""
}
