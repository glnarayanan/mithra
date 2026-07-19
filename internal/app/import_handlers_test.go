package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	importcore "github.com/glnarayanan/mithra/internal/imports"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/storage"
)

func TestCSVImportSendsExtractedTextThenCommitsAtomically(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	scope := ownerScope(t, application, session)
	page := serve(application, coachingGET("/imports", session))
	for _, copy := range []string{"<h1>Import</h1>", "Start with an existing spreadsheet or report.", "Step 1 of 2", "Review file"} {
		if !strings.Contains(page.Body.String(), copy) {
			t.Fatalf("import page missing %q", copy)
		}
	}
	providerCalls := 0
	connectImportProvider(t, application, scope, func(request *http.Request) string {
		providerCalls++
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		input := payload["input"].(string)
		if payload["store"] != false || !strings.Contains(input, "row:2") || strings.Contains(input, "salary,amount,date") {
			t.Fatalf("provider payload = %#v", payload)
		}
		return captureProviderBody(`{"records":[{"family":"finance","locator":{"kind":"row","value":"row:2"},"finance":{"kind":"income","label":"Salary","category":"Income","date":"2026-07-01","end_date":"","status":"","amount":"5000"},"health":null,"planning":null}]}`)
	})
	csv := []byte("label,amount,date\nSalary,5000,2026-07-01\n")
	upload := serve(application, importUploadRequest(t, session, "family.csv", "text/csv", csv, "shared"))
	if upload.Code != http.StatusOK || !strings.Contains(upload.Body.String(), "Review before import") {
		t.Fatalf("upload = %d %q", upload.Code, upload.Body.String())
	}
	resumed := serve(application, authForm(http.MethodGet, "/imports", url.Values{}, []*http.Cookie{session.session, session.csrf}))
	if resumed.Code != http.StatusOK || !strings.Contains(resumed.Body.String(), "Review before import") || !strings.Contains(resumed.Body.String(), "family.csv") || strings.Contains(resumed.Body.String(), `type="hidden" name="action"`) {
		t.Fatalf("resumed review = %d %q", resumed.Code, resumed.Body.String())
	}
	var importID string
	var version int64
	if err := application.db.QueryRow(`SELECT id,version FROM document_imports`).Scan(&importID, &version); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := application.db.QueryRow(`SELECT COUNT(*) FROM finance_income`).Scan(&before); err != nil || before != 0 {
		t.Fatalf("records before review = %d, %v", before, err)
	}
	commit := serve(application, importForm(session, url.Values{"action": {"commit"}, "import_id": {importID}, "version": {"1"}}))
	if commit.Code != http.StatusOK || !strings.Contains(commit.Body.String(), "Import complete") {
		t.Fatalf("commit = %d %q", commit.Code, commit.Body.String())
	}
	var label, generated string
	if err := application.db.QueryRow(`SELECT label,generated_by FROM finance_income WHERE active=1`).Scan(&label, &generated); err != nil || label != "Salary" || generated != "ai" {
		t.Fatalf("record = %q %q, %v", label, generated, err)
	}

	duplicate := serve(application, importUploadRequest(t, session, "renamed.csv", "text/csv", csv, "shared"))
	if duplicate.Code != http.StatusOK || !strings.Contains(duplicate.Body.String(), "Nothing was copied or sent") || providerCalls != 1 {
		t.Fatalf("duplicate = %d calls=%d %q", duplicate.Code, providerCalls, duplicate.Body.String())
	}
	preparedDelete := serve(application, importForm(session, url.Values{"action": {"prepare_delete"}, "import_id": {importID}}))
	deleteMatch := regexp.MustCompile(`name="deletion_token" value="([a-f0-9]+)"`).FindStringSubmatch(preparedDelete.Body.String())
	if !strings.Contains(preparedDelete.Body.String(), "linked to 1 records") || len(deleteMatch) != 2 {
		t.Fatalf("delete impact = %q", preparedDelete.Body.String())
	}
	deleted := serve(application, importForm(session, url.Values{"action": {"delete_confirm"}, "import_id": {importID}, "deletion_token": {deleteMatch[1]}}))
	if !strings.Contains(deleted.Body.String(), "Source deleted") {
		t.Fatalf("delete = %q", deleted.Body.String())
	}
	var active int
	var sourceState string
	if err := application.db.QueryRow(`SELECT active FROM finance_income WHERE label='Salary'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err := application.db.QueryRow(`SELECT state FROM sources LIMIT 1`).Scan(&sourceState); err != nil {
		t.Fatal(err)
	}
	if active != 0 || sourceState != "deleted" {
		t.Fatalf("delete state active=%d source=%q", active, sourceState)
	}
}

func TestImportBlockerRequiresUserCorrectionAndRevisionFence(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	scope := ownerScope(t, application, session)
	connectImportProvider(t, application, scope, func(*http.Request) string {
		return captureProviderBody(`{"records":[{"family":"health","locator":{"kind":"row","value":"row:2"},"finance":null,"health":{"subject":"Alex","analyte":"HbA1c","specimen":"blood","method":"","reference_context":"","observed_on":"2026-07-02","value":"5.8","unit":"","reference_low":"","reference_high":"","reference_unit":""},"planning":null}]}`)
	})
	upload := serve(application, importUploadRequest(t, session, "health.csv", "text/csv", []byte("test,value,date,unit\nHbA1c,5.8,2026-07-02,\n"), "personal"))
	if upload.Code != http.StatusOK || !strings.Contains(upload.Body.String(), "Enter the unit exactly as reported") || !strings.Contains(upload.Body.String(), `aria-invalid="true"`) {
		t.Fatalf("blocked review = %d %q", upload.Code, upload.Body.String())
	}
	var id string
	if err := application.db.QueryRow(`SELECT id FROM document_imports`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	blocked := serve(application, importForm(session, url.Values{"action": {"commit"}, "import_id": {id}, "version": {"1"}}))
	if !strings.Contains(blocked.Body.String(), "required value is unresolved") {
		t.Fatalf("blocked commit = %q", blocked.Body.String())
	}
	var observations int
	_ = application.db.QueryRow(`SELECT COUNT(*) FROM health_observations`).Scan(&observations)
	if observations != 0 {
		t.Fatalf("partial observations = %d", observations)
	}

	values := url.Values{"action": {"correct"}, "import_id": {id}, "version": {"1"}, "field_0-subject": {"Alex"}, "field_0-analyte": {"HbA1c"}, "field_0-observed_on": {"2026-07-02"}, "field_0-value": {"5.8"}, "field_0-unit": {"%"}}
	corrected := serve(application, importForm(session, values))
	if corrected.Code != http.StatusOK || !strings.Contains(corrected.Body.String(), "ready to import") {
		t.Fatalf("correction = %d %q", corrected.Code, corrected.Body.String())
	}
	committed := serve(application, importForm(session, url.Values{"action": {"commit"}, "import_id": {id}, "version": {"2"}}))
	if !strings.Contains(committed.Body.String(), "Import complete") {
		t.Fatalf("corrected commit = %q", committed.Body.String())
	}
	var unit, generated string
	if err := application.db.QueryRow(`SELECT unit,generated_by FROM health_observations WHERE active=1`).Scan(&unit, &generated); err != nil || unit != "%" || generated != "user" {
		t.Fatalf("corrected record = %q %q, %v", unit, generated, err)
	}
}

func TestDiscardCannotDeleteSourceAfterConcurrentPublish(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	scope := ownerScope(t, application, session)
	connectImportProvider(t, application, scope, func(*http.Request) string {
		return captureProviderBody(`{"records":[{"family":"finance","locator":{"kind":"row","value":"row:2"},"finance":{"kind":"income","label":"Salary","category":"Income","date":"2026-07-01","end_date":"","status":"","amount":"5000"},"health":null,"planning":null}]}`)
	})
	if response := serve(application, importUploadRequest(t, session, "family.csv", "text/csv", []byte("label,amount,date\nSalary,5000,2026-07-01\n"), "personal")); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	var importID, sourceID string
	if err := application.db.QueryRow(`SELECT id,source_id FROM document_imports WHERE state='review'`).Scan(&importID, &sourceID); err != nil {
		t.Fatal(err)
	}
	// This trigger models a competing publication after an obsolete cleanup
	// has selected the review. The discard must claim both records atomically.
	if _, err := application.db.Exec(`CREATE TRIGGER publish_during_source_delete BEFORE UPDATE OF state ON sources WHEN NEW.id=` + quoteSQL(sourceID) + ` AND NEW.state='deleted' BEGIN UPDATE document_imports SET state='committed' WHERE id=` + quoteSQL(importID) + `; END`); err != nil {
		t.Fatal(err)
	}
	if err := application.imports.Discard(context.Background(), scope, importID); err == nil {
		t.Fatal("discard unexpectedly won a concurrent publication")
	}
	var sourceState, importState string
	if err := application.db.QueryRow(`SELECT state FROM sources WHERE id=?`, sourceID).Scan(&sourceState); err != nil {
		t.Fatal(err)
	}
	if err := application.db.QueryRow(`SELECT state FROM document_imports WHERE id=?`, importID).Scan(&importState); err != nil {
		t.Fatal(err)
	}
	if sourceState != "live" || importState != "review" {
		t.Fatalf("concurrent discard left source=%q import=%q, want live review", sourceState, importState)
	}
}

func quoteSQL(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

func TestVisualCleanupCannotDeleteSourceAfterConcurrentPublish(t *testing.T) {
	for _, test := range []struct {
		name  string
		state string
		run   func(context.Context, *App, policy.ActorScope, importcore.VisualConsent) error
	}{
		{
			name:  "abort",
			state: "visual_processing",
			run: func(ctx context.Context, application *App, scope policy.ActorScope, consent importcore.VisualConsent) error {
				if err := application.imports.ConsumeVisualConsent(ctx, scope, consent.ID, consent.Token, consent.Version); err != nil {
					return err
				}
				return application.imports.AbortVisual(ctx, scope, consent.ID, consent.Version+1)
			},
		},
		{
			name:  "abandoned",
			state: "awaiting_visual_consent",
			run: func(ctx context.Context, application *App, _ policy.ActorScope, consent importcore.VisualConsent) error {
				if _, err := application.db.Exec(`UPDATE document_imports SET consent_expires_at='2000-01-01T00:00:00Z' WHERE id=?`, consent.ID); err != nil {
					return err
				}
				return application.imports.CleanupAbandonedVisual(ctx)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			application, mailer := newAuthTestApp(t, "owner@example.com")
			session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
			scope := ownerScope(t, application, session)
			source, err := application.sources.Store(context.Background(), scope, []byte("%PDF-1.7\nfixture"), storage.Metadata{Family: "pdf", Version: 1, Visibility: policy.Personal, LocatorKind: "source", LocatorValue: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			consent, err := application.imports.StageVisualConsent(context.Background(), scope, source, "fixture.pdf")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := application.db.Exec(`CREATE TRIGGER publish_during_visual_delete BEFORE UPDATE OF state ON sources WHEN NEW.id=` + quoteSQL(source.ID) + ` AND NEW.state='deleted' BEGIN UPDATE document_imports SET state='committed' WHERE id=` + quoteSQL(consent.ID) + `; END`); err != nil {
				t.Fatal(err)
			}
			if err := test.run(context.Background(), application, scope, consent); err == nil {
				t.Fatal("cleanup unexpectedly won a concurrent publication")
			}
			var sourceState, importState string
			if err := application.db.QueryRow(`SELECT state FROM sources WHERE id=?`, source.ID).Scan(&sourceState); err != nil {
				t.Fatal(err)
			}
			if err := application.db.QueryRow(`SELECT state FROM document_imports WHERE id=?`, consent.ID).Scan(&importState); err != nil {
				t.Fatal(err)
			}
			if sourceState != "live" || importState != test.state {
				t.Fatalf("concurrent cleanup left source=%q import=%q, want live %q", sourceState, importState, test.state)
			}
		})
	}
}

func TestScannedPDFRequiresBoundOneTimeConsentBeforeInlineTransfer(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	application.importExtractor = importcore.New(scannedPDFParser{})
	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	scope := ownerScope(t, application, session)
	providerCalls := 0
	connectImportProvider(t, application, scope, func(request *http.Request) string {
		providerCalls++
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(payload["input"])
		if payload["store"] != false || !strings.Contains(string(encoded), `"type":"input_file"`) || !strings.Contains(string(encoded), "data:application/pdf;base64,") {
			t.Fatalf("visual payload = %#v", payload)
		}
		var processingID string
		if err := application.db.QueryRow(`SELECT id FROM document_imports WHERE state='visual_processing'`).Scan(&processingID); err != nil {
			t.Fatal(err)
		}
		if err := application.imports.Discard(context.Background(), scope, processingID); err == nil {
			t.Fatal("parallel discard deleted a PDF after final transfer authorization")
		}
		return captureProviderBody(`{"records":[{"family":"health","locator":{"kind":"page","value":"page:1"},"finance":null,"health":{"subject":"Alex","analyte":"Vitamin D","specimen":"blood","method":"","reference_context":"","observed_on":"2026-07-02","value":"28","unit":"ng/mL","reference_low":"","reference_high":"","reference_unit":""},"planning":null}]}`)
	})
	pdf := []byte("%PDF-1.7\nscanned fixture")
	prepared := serve(application, importUploadRequest(t, session, "scan.pdf", "application/pdf", pdf, "personal"))
	if prepared.Code != http.StatusOK || !strings.Contains(prepared.Body.String(), "This PDF contains scanned pages") || !strings.Contains(prepared.Body.String(), "Learn about scanned PDFs") || providerCalls != 0 {
		t.Fatalf("visual consent = %d calls=%d %q", prepared.Code, providerCalls, prepared.Body.String())
	}
	var id string
	var version int64
	if err := application.db.QueryRow(`SELECT id,version FROM document_imports WHERE state='awaiting_visual_consent'`).Scan(&id, &version); err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`name="consent_token" value="([a-f0-9]+)"`).FindStringSubmatch(prepared.Body.String())
	if len(match) != 2 {
		t.Fatalf("consent token missing: %q", prepared.Body.String())
	}
	confirmed := serve(application, importForm(session, url.Values{"action": {"visual_confirm"}, "import_id": {id}, "version": {"1"}, "consent_token": {match[1]}}))
	if confirmed.Code != http.StatusOK || !strings.Contains(confirmed.Body.String(), "Review before import") || providerCalls != 1 {
		t.Fatalf("visual confirm = %d calls=%d %q", confirmed.Code, providerCalls, confirmed.Body.String())
	}
	replay := serve(application, importForm(session, url.Values{"action": {"visual_confirm"}, "import_id": {id}, "version": {"1"}, "consent_token": {match[1]}}))
	if !strings.Contains(replay.Body.String(), "expired or changed") || providerCalls != 1 {
		t.Fatalf("visual replay = calls=%d %q", providerCalls, replay.Body.String())
	}
	committed := serve(application, importForm(session, url.Values{"action": {"commit"}, "import_id": {id}, "version": {"3"}}))
	if !strings.Contains(committed.Body.String(), "Import complete") {
		t.Fatalf("visual commit = %q", committed.Body.String())
	}
}

func FuzzVisualImportLifecycleRejectsInvalidTransitions(f *testing.F) {
	f.Add([]byte{4, 9}) // consume v1, then finish v2
	f.Add([]byte{8})    // consume with a stale version
	f.Add([]byte{13})   // finish without a consumed consent

	application, mailer := newAuthTestApp(f, "owner@example.com")
	session := activate(f, application, mailer, "owner@example.com", "an owner secure password", nil)
	scope := ownerScopeForFuzz(f, application, session)
	source, err := application.sources.Store(context.Background(), scope, []byte("%PDF-1.7\nfuzz fixture"), storage.Metadata{Family: "pdf", Version: 1, Visibility: policy.Personal, LocatorKind: "source", LocatorValue: "fixture"})
	if err != nil {
		f.Fatal(err)
	}
	consent, err := application.imports.StageVisualConsent(context.Background(), scope, source, "fixture.pdf")
	if err != nil {
		f.Fatal(err)
	}
	valid := importcore.ProposalSet{Records: []importcore.ProposedRecord{{Family: "health", Locator: importcore.Locator{Kind: "page", Value: "page:1"}, Health: &importcore.HealthProposal{Subject: "Alex", Analyte: "Vitamin D", Specimen: "blood", ObservedOn: "2026-07-02", Value: "28", Unit: "ng/mL"}}}}
	hash := sha256.Sum256([]byte(consent.Token))

	f.Fuzz(func(t *testing.T, actions []byte) {
		if len(actions) > 8 {
			t.Skip()
		}
		if _, err := application.db.Exec(`UPDATE document_imports SET state='awaiting_visual_consent',proposal_json='',version=1,consent_token_hash=?,consent_expires_at=?,updated_at=? WHERE id=?`, hex.EncodeToString(hash[:]), time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), consent.ID); err != nil {
			t.Fatal(err)
		}
		state, version := "awaiting_visual_consent", int64(1)
		for _, action := range actions {
			expected := int64((action >> 2) & 3)
			switch action & 3 {
			case 0:
				token := consent.Token
				if action&0x10 != 0 {
					token = "wrong-token"
				}
				allowed := state == "awaiting_visual_consent" && version == expected && token == consent.Token
				err := application.imports.ConsumeVisualConsent(context.Background(), scope, consent.ID, token, expected)
				if allowed && err != nil {
					t.Fatalf("consume valid transition: %v", err)
				}
				if !allowed && err == nil {
					t.Fatal("consume accepted an invalid lifecycle transition")
				}
				if allowed {
					state, version = "visual_processing", version+1
				}
			case 1:
				allowed := state == "visual_processing" && version == expected
				_, err := application.imports.FinishVisual(context.Background(), scope, consent.ID, expected, valid)
				if allowed && err != nil {
					t.Fatalf("finish valid transition: %v", err)
				}
				if !allowed && err == nil {
					t.Fatal("finish accepted an invalid lifecycle transition")
				}
				if allowed {
					state, version = "review", version+1
				}
			}
		}
		var gotState string
		var gotVersion int64
		if err := application.db.QueryRow(`SELECT state,version FROM document_imports WHERE id=?`, consent.ID).Scan(&gotState, &gotVersion); err != nil || gotState != state || gotVersion != version {
			t.Fatalf("lifecycle = %q/%d, want %q/%d, err=%v", gotState, gotVersion, state, version, err)
		}
		var sourceState string
		if err := application.db.QueryRow(`SELECT state FROM sources WHERE id=?`, source.ID).Scan(&sourceState); err != nil || sourceState != "live" {
			t.Fatalf("transition changed source state=%q err=%v", sourceState, err)
		}
	})
}

func ownerScopeForFuzz(t testing.TB, application *App, session browserSession) policy.ActorScope {
	t.Helper()
	scope, err := application.auth.Authenticate(context.Background(), session.session.Value)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func TestExplicitChangedVersionReplacesOnlyChosenPriorRecords(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	scope := ownerScope(t, application, session)
	amount := "5000"
	connectImportProvider(t, application, scope, func(*http.Request) string {
		return captureProviderBody(`{"records":[{"family":"finance","locator":{"kind":"row","value":"row:2"},"finance":{"kind":"income","label":"Salary","category":"Income","date":"2026-07-01","end_date":"","status":"","amount":"` + amount + `"},"health":null,"planning":null}]}`)
	})
	first := serve(application, importUploadRequest(t, session, "first.csv", "text/csv", []byte("label,amount,date\nSalary,5000,2026-07-01\n"), "shared"))
	if first.Code != http.StatusOK {
		t.Fatal(first.Body.String())
	}
	var firstID string
	if err := application.db.QueryRow(`SELECT id FROM document_imports WHERE state='review'`).Scan(&firstID); err != nil {
		t.Fatal(err)
	}
	serve(application, importForm(session, url.Values{"action": {"commit"}, "import_id": {firstID}, "version": {"1"}}))
	amount = "6000"
	second := serve(application, importUploadRequest(t, session, "unrelated-name.csv", "text/csv", []byte("label,amount,date\nSalary,6000,2026-07-01\n"), "shared", firstID))
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), "Review before import") {
		t.Fatalf("successor upload = %q", second.Body.String())
	}
	var secondID string
	if err := application.db.QueryRow(`SELECT id FROM document_imports WHERE state='review'`).Scan(&secondID); err != nil {
		t.Fatal(err)
	}
	committed := serve(application, importForm(session, url.Values{"action": {"commit"}, "import_id": {secondID}, "version": {"1"}}))
	if !strings.Contains(committed.Body.String(), "Import complete") {
		t.Fatalf("successor commit = %q", committed.Body.String())
	}
	var active, total int
	var priorState string
	_ = application.db.QueryRow(`SELECT COUNT(*) FROM finance_income WHERE active=1 AND amount_original='6000'`).Scan(&active)
	_ = application.db.QueryRow(`SELECT COUNT(*) FROM finance_income`).Scan(&total)
	_ = application.db.QueryRow(`SELECT state FROM document_imports WHERE id=?`, firstID).Scan(&priorState)
	if active != 1 || total != 2 || priorState != "superseded" {
		t.Fatalf("successor active=%d total=%d prior=%q", active, total, priorState)
	}
}

type scannedPDFParser struct{}

func (scannedPDFParser) Extract(context.Context, []byte, importcore.Limits) ([]importcore.Fragment, error) {
	return nil, importcore.ErrScannedPDF
}

func connectImportProvider(t *testing.T, application *App, scope policy.ActorScope, response func(*http.Request) string) {
	t.Helper()
	if err := application.providerSettings.ReplaceOpenAI(context.Background(), scope, "sk-import-test-secret", func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	application.openAIClient = &http.Client{Transport: appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response(request))), Request: request}, nil
	})}
}

func importUploadRequest(t *testing.T, session browserSession, name, contentType string, content []byte, visibility string, replacement ...string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("csrf", session.csrf.Value)
	_ = writer.WriteField("action", "upload")
	_ = writer.WriteField("visibility", visibility)
	if len(replacement) == 1 {
		_ = writer.WriteField("replaces_import_id", replacement[0])
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; filename="`+name+`"`)
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/imports", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Origin", testOrigin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(session.session)
	request.AddCookie(session.csrf)
	return request
}

func importForm(session browserSession, values url.Values) *http.Request {
	values.Set("csrf", session.csrf.Value)
	return authForm(http.MethodPost, "/imports", values, []*http.Cookie{session.session, session.csrf})
}
