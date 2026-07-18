package capture

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/finance"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/storage"
)

func newFixture(t *testing.T) (*Service, *storage.Service, policy.ActorScope, policy.ActorScope, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "mithra.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	stamp := "2026-07-18T00:00:00Z"
	for _, v := range [][]string{{"owner", "owner@example.com"}, {"partner", "partner@example.com"}} {
		if _, err := db.Exec(`INSERT INTO users(id,email,status,password_hash,created_at,updated_at) VALUES(?,?,'active','hash',?,?)`, v[0], v[1], stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO households(id,status,owner_user_id,created_at,updated_at) VALUES('home','active','owner',?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	for _, v := range [][]string{{"owner", "owner"}, {"partner", "adult"}} {
		if _, err := db.Exec(`INSERT INTO household_members(household_id,user_id,role,created_at) VALUES('home',?,?,?)`, v[0], v[1], stamp); err != nil {
			t.Fatal(err)
		}
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	store, err := storage.New(db, filepath.Join(t.TempDir(), "sources"), key)
	if err != nil {
		t.Fatal(err)
	}
	return New(db, store), store, policy.ActorScope{ActorID: "owner", HouseholdID: "home", Role: "owner"}, policy.ActorScope{ActorID: "partner", HouseholdID: "home", Role: "adult"}, db
}

func TestClearTypedCapturesAndClarification(t *testing.T) {
	s, _, owner, _, db := newFixture(t)
	ctx := context.Background()
	financeCapture, err := s.SubmitText(ctx, owner, TextRequest{Text: "Bought groceries", Visibility: policy.Shared, Proposal: Proposal{Variant: FinanceVariant, Finance: &FinanceProposal{Kind: finance.Spending, Label: "Groceries", Category: "Food", Date: "2026-07-18", AmountText: "42.50"}}})
	if err != nil || financeCapture.RecordFamily != "finance" || financeCapture.SourceID == "" {
		t.Fatalf("finance capture = %#v, %v", financeCapture, err)
	}
	healthCapture, err := s.SubmitText(ctx, owner, TextRequest{Text: "Blood pressure", Visibility: policy.Personal, Proposal: Proposal{Variant: HealthVariant, Health: &HealthProposal{Kind: Observation, Subject: "owner", Analyte: "Blood pressure", ObservedOn: "2026-07-18", Value: "120", Unit: "mmHg"}}})
	if err != nil || healthCapture.RecordFamily != "health" {
		t.Fatalf("health capture = %#v, %v", healthCapture, err)
	}
	planningCapture, err := s.SubmitText(ctx, owner, TextRequest{Text: "Family dinner", Visibility: policy.Shared, Proposal: Proposal{Variant: PlanningVariant, Planning: &PlanningProposal{Title: "Family dinner", AllDay: true, StartsOn: "2026-07-19", Status: "planned"}}})
	if err != nil || planningCapture.RecordFamily != "planning" {
		t.Fatalf("planning capture = %#v, %v", planningCapture, err)
	}
	clarified, err := s.SubmitText(ctx, owner, TextRequest{Text: "Bought milk", Visibility: policy.Shared, Proposal: Proposal{Variant: FinanceVariant, Finance: &FinanceProposal{Kind: finance.Spending, Label: "Milk", AmountText: "3"}}})
	if err != nil || clarified.State != "clarification" || clarified.ClarificationField != "date" || clarified.RecordID != "" {
		t.Fatalf("clarification = %#v, %v", clarified, err)
	}
	var records int
	if err := db.QueryRow(`SELECT COUNT(*) FROM finance_spending WHERE label='Milk'`).Scan(&records); err != nil || records != 0 {
		t.Fatalf("ambiguous records = %d, %v", records, err)
	}
}

func TestRejectsHostileProposalAndCleansRawAudio(t *testing.T) {
	s, store, owner, _, _ := newFixture(t)
	ctx := context.Background()
	if _, err := s.SubmitText(ctx, owner, TextRequest{Text: "<script>alert(1)</script>", Visibility: policy.Shared, Proposal: Proposal{Variant: PlanningVariant, Planning: &PlanningProposal{Title: "<script>", AllDay: true, StartsOn: "2026-07-19", Status: "planned"}}}); !errors.Is(err, ErrInvalidProposal) {
		t.Fatalf("hostile proposal error = %v", err)
	}
	audio, err := s.StageAudio(ctx, owner, AudioRequest{Bytes: []byte("webm"), Visibility: policy.Shared})
	if err != nil {
		t.Fatal(err)
	}
	transcript, err := s.SubmitTranscript(ctx, owner, audio.ID, TextRequest{Text: "Pay electric bill", Visibility: policy.Personal, Proposal: Proposal{Variant: FinanceVariant, Finance: &FinanceProposal{Kind: finance.Obligation, Label: "Electricity", Date: "2026-07-20", Status: "pending", AmountText: "50"}}})
	if err != nil {
		t.Fatal(err)
	}
	if transcript.Visibility != policy.Shared || transcript.RawAudioSourceID != audio.RawAudioSourceID {
		t.Fatalf("transcript scope/raw = %#v", transcript)
	}
	if err := s.Confirm(ctx, owner, audio.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Read(ctx, owner, audio.RawAudioSourceID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("raw audio still readable: %v", err)
	}
	cancelled, err := s.StageAudio(ctx, owner, AudioRequest{Bytes: []byte("webm"), Visibility: policy.Personal})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CancelAudio(ctx, owner, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Cleanup(ctx, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Read(ctx, owner, cancelled.RawAudioSourceID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("cancelled raw audio still readable: %v", err)
	}
}

func TestAnswerClarificationCommitsOnlyAfterTheMissingField(t *testing.T) {
	s, _, owner, _, _ := newFixture(t)
	ctx := context.Background()
	c, err := s.SubmitText(ctx, owner, TextRequest{Text: "Bought milk", Visibility: policy.Personal, Proposal: Proposal{Variant: FinanceVariant, Finance: &FinanceProposal{Kind: finance.Spending, Label: "Milk", AmountText: "3"}}})
	if err != nil || c.ClarificationField != "date" {
		t.Fatalf("clarification = %#v, %v", c, err)
	}
	if err := s.Confirm(ctx, owner, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("confirmation before record = %v", err)
	}
	completed, err := s.AnswerClarification(ctx, owner, c.ID, "2026-07-18")
	if err != nil || completed.RecordID == "" || completed.State != "awaiting_confirmation" {
		t.Fatalf("answered capture = %#v, %v", completed, err)
	}
}

func TestUndoRefusesAfterPartnerRevisionAndAllowsUntouchedPersonal(t *testing.T) {
	s, _, owner, partner, _ := newFixture(t)
	ctx := context.Background()
	shared, err := s.SubmitText(ctx, owner, TextRequest{Text: "Groceries", Visibility: policy.Shared, Proposal: Proposal{Variant: FinanceVariant, Finance: &FinanceProposal{Kind: finance.Spending, Label: "Groceries", Date: "2026-07-18", AmountText: "10"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := finance.New(s.db).Correct(ctx, partner, finance.Spending, shared.RecordID, 1, finance.Draft{Visibility: policy.Shared, Label: "Groceries", Date: "2026-07-18", AmountText: "11", Provenance: finance.Provenance{SourceID: shared.SourceID, SourceFamily: "text", SourceVersion: 1, LocatorKind: "source", LocatorValue: shared.SourceID}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Undo(ctx, owner, shared.ID); !errors.Is(err, ErrUndoRefused) {
		t.Fatalf("partner-edited undo = %v", err)
	}
	personal, err := s.SubmitText(ctx, owner, TextRequest{Text: "Coffee", Visibility: policy.Personal, Proposal: Proposal{Variant: FinanceVariant, Finance: &FinanceProposal{Kind: finance.Spending, Label: "Coffee", Date: "2026-07-18", AmountText: "3"}}})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Now().UTC() }
	if err := s.Undo(ctx, owner, personal.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDiscardClarificationDeletesSourceAndListDoesNotHoldConnection(t *testing.T) {
	s, store, owner, partner, db := newFixture(t)
	ctx := context.Background()
	receipt, err := s.SubmitText(ctx, owner, TextRequest{Text: "Electricity bill", Visibility: policy.Shared, Proposal: Proposal{Variant: FinanceVariant, Finance: &FinanceProposal{Kind: finance.Obligation, Label: "Electricity", Date: "2026-07-20", AmountText: "50"}}})
	if err != nil || receipt.State != "clarification" || receipt.ClarificationField != "status" {
		t.Fatalf("clarification = %#v, %v", receipt, err)
	}
	listed, err := s.List(ctx, owner, 20)
	if err != nil || len(listed) != 1 || listed[0].ID != receipt.ID {
		t.Fatalf("list = %#v, %v", listed, err)
	}
	if err := store.Delete(ctx, partner, receipt.SourceID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("partner deleted shared source: %v", err)
	}
	if err := s.Discard(ctx, owner, receipt.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Read(ctx, owner, receipt.SourceID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("discarded source readable: %v", err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM captures WHERE id=?`, receipt.ID).Scan(&state); err != nil || state != "cancelled" {
		t.Fatalf("discard state = %q, %v", state, err)
	}
}

func TestClarificationAnswerBecomesVersionedEvidence(t *testing.T) {
	s, store, owner, _, _ := newFixture(t)
	ctx := context.Background()
	receipt, err := s.SubmitText(ctx, owner, TextRequest{Text: "Bought milk for 85", Summary: "Milk purchase", Visibility: policy.Personal, Proposal: Proposal{Variant: FinanceVariant, Finance: &FinanceProposal{Kind: finance.Spending, Label: "Milk", AmountText: "85"}}})
	if err != nil || receipt.State != "clarification" {
		t.Fatalf("clarification = %#v, %v", receipt, err)
	}
	resolved, err := s.AnswerClarification(ctx, owner, receipt.ID, "2026-07-18")
	if err != nil || resolved.State != "awaiting_confirmation" || resolved.SourceID == receipt.SourceID {
		t.Fatalf("resolved = %#v, %v", resolved, err)
	}
	text, source, err := store.Read(ctx, owner, resolved.SourceID)
	if err != nil || source.Version != 2 || !strings.Contains(string(text), "User answered: 2026-07-18") {
		clear(text)
		t.Fatalf("revised evidence version=%d text=%q err=%v", source.Version, text, err)
	}
	clear(text)
	if _, _, err := store.Read(ctx, owner, receipt.SourceID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("superseded source still readable: %v", err)
	}
}

func TestExpiredAudioCleanupSurvivesMembershipLoss(t *testing.T) {
	s, _, owner, _, db := newFixture(t)
	ctx := context.Background()
	receipt, err := s.StageAudio(ctx, owner, AudioRequest{Bytes: []byte("voice bytes"), Visibility: policy.Personal})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE users SET status='disabled' WHERE id=?`, owner.ActorID); err != nil {
		t.Fatal(err)
	}
	if err := s.Cleanup(ctx, time.Now().Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var sourceState string
	if err := db.QueryRow(`SELECT state FROM sources WHERE id=?`, receipt.RawAudioSourceID).Scan(&sourceState); err != nil || sourceState != "deleted" {
		t.Fatalf("expired source state = %q, %v", sourceState, err)
	}
	var raw any
	if err := db.QueryRow(`SELECT raw_audio_source_id FROM captures WHERE id=?`, receipt.ID).Scan(&raw); err != nil || raw != nil {
		t.Fatalf("expired raw reference = %#v, %v", raw, err)
	}
}
