package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/capture"
	"github.com/glnarayanan/mithra/internal/demo"
	"github.com/glnarayanan/mithra/internal/finance"
	"github.com/glnarayanan/mithra/internal/health"
	"github.com/glnarayanan/mithra/internal/imports"
	"github.com/glnarayanan/mithra/internal/planning"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/storage"
)

func TestReleaseCandidateSeedAndArbitraryHouseholdSurviveRestartThroughSameServices(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	databasePath := filepath.Join(dataRoot, "mithra.sqlite3")
	sourceRoot := filepath.Join(dataRoot, "sources")
	key := acceptanceKey()
	if _, err := demo.Reset(ctx, demo.Config{DatabasePath: databasePath, SourceRoot: sourceRoot, BackupRoot: filepath.Join(root, "backups"), OwnerEmail: "judge-owner@example.com", PartnerEmail: "judge-partner@example.com", MasterKey: key}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{DatabasePath: databasePath, SourceRoot: sourceRoot, MasterKey: key, CanonicalOrigin: "http://127.0.0.1:18099", AllowedEmails: []string{"judge-owner@example.com", "judge-partner@example.com", "arbitrary@example.com"}}
	application, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := application.db.ExecContext(ctx, `UPDATE users SET status='active',updated_at=? WHERE email='arbitrary@example.com'; INSERT INTO households(id,status,owner_user_id,timezone,created_at,updated_at) SELECT 'arbitrary-household','active',id,'Asia/Kolkata',?,? FROM users WHERE email='arbitrary@example.com'; INSERT INTO household_members(household_id,user_id,role,created_at) SELECT 'arbitrary-household',id,'owner',? FROM users WHERE email='arbitrary@example.com'`, stamp, stamp, stamp, stamp); err != nil {
		application.Close()
		t.Fatal(err)
	}
	var arbitraryID string
	if err := application.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email='arbitrary@example.com'`).Scan(&arbitraryID); err != nil {
		application.Close()
		t.Fatal(err)
	}
	actor := policy.ActorScope{ActorID: arbitraryID, HouseholdID: "arbitrary-household", Role: "owner"}
	if err := importArbitraryFinance(ctx, application, actor); err != nil {
		application.Close()
		t.Fatal(err)
	}
	if err := importArbitraryHealth(ctx, application, actor); err != nil {
		application.Close()
		t.Fatal(err)
	}
	receipt, err := application.captureRecords.SubmitText(ctx, actor, capture.TextRequest{Text: "Add a shared call with the school next Monday.", Summary: "School call added.", Visibility: policy.Shared, Proposal: capture.Proposal{Variant: capture.PlanningVariant, Planning: &capture.PlanningProposal{Title: "Call the school", StartsAt: "2026-07-27T16:00", EndsAt: "2026-07-27T16:30", Timezone: "Asia/Kolkata", Status: "planned"}}})
	if err != nil || application.captureRecords.Confirm(ctx, actor, receipt.ID) != nil {
		application.Close()
		t.Fatalf("planning capture: receipt=%+v err=%v", receipt, err)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	financeSummary, err := restarted.finance.Summarize(ctx, actor, finance.AllRecords, time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC))
	if err != nil || len(financeSummary.Records) != 2 {
		t.Fatalf("finance after restart records=%d err=%v", len(financeSummary.Records), err)
	}
	healthSummary, err := restarted.healthRecords.Summarize(ctx, actor, health.AllRecords)
	if err != nil || len(healthSummary.Observations) != 1 {
		t.Fatalf("health after restart observations=%d err=%v", len(healthSummary.Observations), err)
	}
	events, err := restarted.planningRecords.Events(ctx, actor, planning.AllRecords, "2026-07-01", "2026-08-01")
	if err != nil || len(events) != 1 {
		t.Fatalf("planning after restart events=%d err=%v", len(events), err)
	}
	overview, err := restarted.coaching.Overview(ctx, actor, time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC))
	if err != nil || !overview.HasRecords || len(overview.SharedContext.Facts) < 4 {
		t.Fatalf("Family Brief after restart facts=%d err=%v", len(overview.SharedContext.Facts), err)
	}
	var demoOwner string
	if err := restarted.db.QueryRowContext(ctx, `SELECT owner_user_id FROM demo_households WHERE household_id=?`, demo.HouseholdID).Scan(&demoOwner); err != nil || demoOwner == "" {
		t.Fatalf("seed disappeared after arbitrary import owner=%q err=%v", demoOwner, err)
	}
	forged := policy.ActorScope{ActorID: demoOwner, HouseholdID: actor.HouseholdID, Role: "owner"}
	if _, err := restarted.finance.List(ctx, forged, finance.AllRecords); !errors.Is(err, policy.ErrUnauthorized) {
		t.Fatalf("cross-household finance read = %v", err)
	}
}

func importArbitraryFinance(ctx context.Context, application *App, actor policy.ActorScope) error {
	content := []byte("kind,label,category,date,amount\nasset,Emergency savings,Savings,2026-07-01,250000\nspending,Utilities,Home,2026-07-05,7800\n")
	if document, err := imports.New(nil).Extract(ctx, imports.Input{Name: "my-finances.csv", ContentType: "text/csv", Bytes: content}); err != nil || len(document.Fragments) != 3 {
		return errors.Join(errors.New("extract arbitrary finance CSV"), err)
	}
	source, err := application.sources.Store(ctx, actor, content, storage.Metadata{Family: "csv", Version: 1, Visibility: policy.Shared, LocatorKind: "source", LocatorValue: "arbitrary-finance"})
	if err != nil {
		return err
	}
	review, err := application.imports.Stage(ctx, actor, source, "my-finances.csv", imports.ProposalSet{Records: []imports.ProposedRecord{
		{Family: "finance", Locator: imports.Locator{Kind: "row", Value: "row:2"}, Finance: &imports.FinanceProposal{Kind: "asset", Label: "Emergency savings", Category: "Savings", Date: "2026-07-01", Amount: "250000"}},
		{Family: "finance", Locator: imports.Locator{Kind: "row", Value: "row:3"}, Finance: &imports.FinanceProposal{Kind: "spending", Label: "Utilities", Category: "Home", Date: "2026-07-05", Amount: "7800"}},
	}}, "")
	if err != nil {
		return err
	}
	return application.imports.Commit(ctx, actor, review.ID, review.Version)
}

func importArbitraryHealth(ctx context.Context, application *App, actor policy.ActorScope) error {
	content := acceptancePDF("2026-07-08 Vitamin D 32 ng/mL")
	if document, err := imports.New(nil).Extract(ctx, imports.Input{Name: "my-health.pdf", ContentType: "application/pdf", Bytes: content}); err != nil || len(document.Fragments) != 1 {
		return errors.Join(errors.New("extract arbitrary health PDF"), err)
	}
	source, err := application.sources.Store(ctx, actor, content, storage.Metadata{Family: "pdf", Version: 1, Visibility: policy.Shared, LocatorKind: "source", LocatorValue: "arbitrary-health"})
	if err != nil {
		return err
	}
	review, err := application.imports.Stage(ctx, actor, source, "my-health.pdf", imports.ProposalSet{Records: []imports.ProposedRecord{{Family: "health", Locator: imports.Locator{Kind: "page", Value: "page:1"}, Health: &imports.HealthProposal{Subject: "Self", Analyte: "Vitamin D", ObservedOn: "2026-07-08", Value: "32", Unit: "ng/mL", ReferenceLow: "20", ReferenceHigh: "50", ReferenceUnit: "ng/mL"}}}}, "")
	if err != nil {
		return err
	}
	return application.imports.Commit(ctx, actor, review.ID, review.Version)
}

func acceptancePDF(text string) []byte {
	stream := fmt.Sprintf("BT /F1 11 Tf 72 720 Td (%s) Tj ET\n", text)
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>", fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream), "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}
	var output bytes.Buffer
	output.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		offsets[index+1] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := output.Len()
	fmt.Fprintf(&output, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&output, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&output, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return output.Bytes()
}

func acceptanceKey() []byte {
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(200 - index)
	}
	return key
}
