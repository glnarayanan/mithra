package imports

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/finance"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/xuri/excelize/v2"
)

func TestCSVExtractionIsBoundedAndLocatorBearing(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "testdata", "imports", "finance", "household.csv"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := New(nil).Extract(context.Background(), Input{Name: "household.csv", ContentType: "text/csv", Bytes: content})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Kind != CSV || doc.MIME != "text/csv" || len(doc.Digest) != 64 {
		t.Fatalf("document = %#v", doc)
	}
	if doc.Fragments[0].Locator != (Locator{Kind: "row", Value: "row:1"}) {
		t.Fatalf("first locator = %#v", doc.Fragments[0].Locator)
	}
	if !strings.Contains(doc.MappingText, "row:2: income") || len(doc.MappingText) > MaxMappingText {
		t.Fatalf("mapping text = %q", doc.MappingText)
	}
	_, err = New(nil).Extract(context.Background(), Input{Name: "household.csv", ContentType: "application/pdf", Bytes: content})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("claimed MIME error = %v", err)
	}
	_, err = New(nil).Extract(context.Background(), Input{Name: "large.csv", Bytes: make([]byte, MaxFileBytes+1)})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("size error = %v", err)
	}
}

func TestXLSXExtractionHandlesSparseFormulaAndDateCells(t *testing.T) {
	file := excelize.NewFile()
	defer file.Close()
	if err := file.SetCellValue("Sheet1", "A1", "date"); err != nil {
		t.Fatal(err)
	}
	if err := file.SetCellValue("Sheet1", "B1", "amount"); err != nil {
		t.Fatal(err)
	}
	if err := file.SetCellValue("Sheet1", "A2", time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	style, err := file.NewStyle(&excelize.Style{NumFmt: 14})
	if err != nil {
		t.Fatal(err)
	}
	if err := file.SetCellStyle("Sheet1", "A2", "A2", style); err != nil {
		t.Fatal(err)
	}
	if err := file.SetCellValue("Sheet1", "B2", 12); err != nil {
		t.Fatal(err)
	}
	if err := file.SetCellFormula("Sheet1", "D4", "=B2*2"); err != nil {
		t.Fatal(err)
	}
	var content bytes.Buffer
	if err := file.Write(&content); err != nil {
		t.Fatal(err)
	}
	doc, err := New(nil).Extract(context.Background(), Input{Name: "book.xlsx", ContentType: spreadsheetMIME, Bytes: content.Bytes()})
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, fragment := range doc.Fragments {
		values[fragment.Locator.Value] = fragment.Text
	}
	if values["Sheet1!A2"] == "" || values["Sheet1!D4"] != "=B2*2" {
		t.Fatalf("extracted values = %#v", values)
	}
	if _, err := New(nil).Extract(context.Background(), Input{Name: "book.xlsx", Bytes: []byte("PK\x03\x04not-a-workbook")}); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("corrupt xlsx error = %v", err)
	}
}

func TestPDFClassificationsAndBoundedParser(t *testing.T) {
	parser := fakePDF{fragments: []Fragment{{Locator: Locator{Kind: "page", Value: "page:2"}, Text: "report"}}}
	doc, err := New(parser).Extract(context.Background(), Input{Name: "report.pdf", ContentType: "application/pdf", Bytes: []byte("%PDF-1.7\n")})
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Fragments[0].Locator.Value; got != "page:2" {
		t.Fatalf("locator = %q", got)
	}
	for _, test := range []struct {
		name   string
		parser PDFParser
		want   error
	}{
		{"scanned", fakePDF{}, ErrScannedPDF},
		{"over-limit", fakePDF{err: ErrOverLimit}, ErrOverLimit},
		{"panic", panicPDF{}, ErrParserPanic},
		{"unreadable", fakePDF{err: errors.New("bad")}, ErrUnreadable},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.parser).Extract(context.Background(), Input{Name: "report.pdf", Bytes: []byte("%PDF-1.7\n")})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	timeoutContext, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, err = New(blockingPDF{}).Extract(timeoutContext, Input{Name: "report.pdf", Bytes: []byte("%PDF-1.7\n")})
	if !errors.Is(err, ErrParserTimeout) {
		t.Fatalf("timeout error = %v", err)
	}
	_, err = New(nil).Extract(context.Background(), Input{Name: "locked.pdf", Bytes: []byte("%PDF-1.7\n/Encrypt")})
	if !errors.Is(err, ErrEncryptedPDF) {
		t.Fatalf("encrypted error = %v", err)
	}
}

func TestDraftAndDuplicatePrimitives(t *testing.T) {
	source := SourceRef{ID: "source-1", Family: "csv", Version: 1}
	draft, err := FinanceDraft(source, Locator{Kind: "row", Value: "row:2"}, finance.Draft{Kind: finance.Spending, Label: "Groceries"})
	if err != nil || draft.Provenance.SourceID != source.ID || draft.Provenance.LocatorValue != "row:2" {
		t.Fatalf("draft = %#v, %v", draft, err)
	}
	actor := policy.ActorScope{ActorID: "user", HouseholdID: "home", Role: "owner"}
	digest := strings.Repeat("a", 64)
	key, err := ExactDuplicateKey(actor, policy.Personal, digest)
	if err != nil {
		t.Fatal(err)
	}
	token, err := NewSuccessorToken("source-1", digest, key)
	if err != nil || !token.Matches(key, strings.Repeat("b", 64)) || token.Matches(key, digest) {
		t.Fatalf("token = %#v, %v", token, err)
	}
	other, _ := ExactDuplicateKey(policy.ActorScope{ActorID: "partner", HouseholdID: "home", Role: "adult"}, policy.Personal, digest)
	if key == other || token.Matches(other, strings.Repeat("b", 64)) {
		t.Fatal("duplicate scope leaked")
	}
}

type fakePDF struct {
	fragments []Fragment
	err       error
}

func (f fakePDF) Extract(context.Context, []byte, Limits) ([]Fragment, error) {
	return f.fragments, f.err
}

type panicPDF struct{}

func (panicPDF) Extract(context.Context, []byte, Limits) ([]Fragment, error) { panic("boom") }

type blockingPDF struct{}

func (blockingPDF) Extract(context.Context, []byte, Limits) ([]Fragment, error) { select {} }
