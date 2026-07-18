// Package imports extracts bounded, local text from supported document imports.
package imports

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/glnarayanan/mithra/internal/finance"
	"github.com/glnarayanan/mithra/internal/health"
	"github.com/glnarayanan/mithra/internal/planning"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/xuri/excelize/v2"
)

const (
	MaxFileBytes    = 10 << 20
	MaxZIPExpanded  = 50 << 20
	MaxRows         = 10_000
	MaxCells        = 100_000
	MaxPDFPages     = 200
	MaxPDFTextBytes = 2 << 20
	MaxPDFDuration  = 5 * time.Second
	MaxMappingText  = 64 << 10
	maxZIPMembers   = 1_024
	spreadsheetMIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
)

var (
	ErrInvalidInput  = errors.New("invalid import input")
	ErrOverLimit     = errors.New("import exceeds a safety limit")
	ErrUnsupported   = errors.New("unsupported import format")
	ErrUnreadable    = errors.New("import is unreadable")
	ErrScannedPDF    = errors.New("PDF has no extractable text")
	ErrEncryptedPDF  = errors.New("encrypted PDF is unsupported")
	ErrParserPanic   = errors.New("PDF parser panicked")
	ErrParserTimeout = errors.New("PDF parser timed out")
)

type Kind string

const (
	CSV  Kind = "csv"
	XLSX Kind = "xlsx"
	PDF  Kind = "pdf"
)

type Locator struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type Fragment struct {
	Locator Locator
	Text    string
}

type Input struct {
	Name        string
	ContentType string
	Bytes       []byte
}

// Document is the local-only extraction result. Text is deliberately capped for
// mapping; callers retain the encrypted original as the authoritative evidence.
type Document struct {
	Kind        Kind
	MIME        string
	Digest      string
	Fragments   []Fragment
	MappingText string
}

type Limits struct {
	MaxPages int
	MaxText  int
}

type PDFParser interface {
	Extract(context.Context, []byte, Limits) ([]Fragment, error)
}

type Extractor struct{ PDF PDFParser }

func New(pdf PDFParser) Extractor {
	if pdf == nil {
		pdf = LocalPDFParser{}
	}
	return Extractor{PDF: pdf}
}

func (e Extractor) Extract(ctx context.Context, input Input) (Document, error) {
	if ctx == nil || len(input.Bytes) == 0 || len(input.Bytes) > MaxFileBytes || strings.TrimSpace(input.Name) == "" {
		return Document{}, ErrInvalidInput
	}
	kind, mime, err := classify(input)
	if err != nil {
		return Document{}, err
	}
	document := Document{Kind: kind, MIME: mime, Digest: digest(input.Bytes)}
	switch kind {
	case CSV:
		document.Fragments, err = extractCSV(input.Bytes)
	case XLSX:
		document.Fragments, err = extractXLSX(input.Bytes)
	case PDF:
		parser := e.PDF
		if parser == nil {
			parser = LocalPDFParser{}
		}
		document.Fragments, err = boundedPDF(ctx, parser, input.Bytes)
	}
	if err != nil {
		return Document{}, err
	}
	document.MappingText = mappingText(document.Fragments)
	return document, nil
}

func classify(input Input) (Kind, string, error) {
	ext := strings.ToLower(filepath.Ext(input.Name))
	claimed := strings.ToLower(strings.TrimSpace(strings.Split(input.ContentType, ";")[0]))
	match := func(kind Kind, mime string, extensions ...string) (Kind, string, error) {
		for _, extension := range extensions {
			if ext == extension {
				if claimed != "" && claimed != mime {
					return "", "", ErrUnsupported
				}
				return kind, mime, nil
			}
		}
		return "", "", ErrUnsupported
	}
	switch {
	case bytes.HasPrefix(input.Bytes, []byte("%PDF-")):
		return match(PDF, "application/pdf", ".pdf")
	case bytes.HasPrefix(input.Bytes, []byte("PK\x03\x04")):
		if err := preflightXLSX(input.Bytes); err != nil {
			return "", "", err
		}
		return match(XLSX, spreadsheetMIME, ".xlsx")
	default:
		if ext != ".csv" || (claimed != "" && claimed != "text/csv") || !utf8.Valid(input.Bytes) || bytes.IndexByte(input.Bytes, 0) >= 0 {
			return "", "", ErrUnsupported
		}
		return CSV, "text/csv", nil
	}
}

func extractCSV(content []byte) ([]Fragment, error) {
	reader := csv.NewReader(bytes.NewReader(content))
	reader.FieldsPerRecord = -1
	var fragments []Fragment
	cells := 0
	for row := 1; ; row++ {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return fragments, nil
		}
		if err != nil {
			return nil, ErrUnreadable
		}
		if row > MaxRows {
			return nil, ErrOverLimit
		}
		cells += len(record)
		if len(record) > MaxCells || cells > MaxCells {
			return nil, ErrOverLimit
		}
		fragments = append(fragments, Fragment{Locator: Locator{Kind: "row", Value: fmt.Sprintf("row:%d", row)}, Text: strings.Join(record, " | ")})
	}
}

func preflightXLSX(content []byte) error {
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return ErrUnreadable
	}
	var expanded int64
	seenContent, seenWorkbook := false, false
	for _, file := range reader.File {
		if len(reader.File) > maxZIPMembers || file.UncompressedSize64 > uint64(MaxZIPExpanded) || int64(file.UncompressedSize64) > MaxZIPExpanded-expanded {
			return ErrOverLimit
		}
		expanded += int64(file.UncompressedSize64)
		switch file.Name {
		case "[Content_Types].xml":
			seenContent = true
		case "xl/workbook.xml":
			seenWorkbook = true
		}
	}
	if !seenContent || !seenWorkbook {
		return ErrUnsupported
	}
	return nil
}

func extractXLSX(content []byte) ([]Fragment, error) {
	file, err := excelize.OpenReader(bytes.NewReader(content), excelize.Options{UnzipSizeLimit: MaxZIPExpanded, UnzipXMLSizeLimit: MaxZIPExpanded})
	if err != nil {
		return nil, ErrUnreadable
	}
	defer file.Close()
	var fragments []Fragment
	rows, cells := 0, 0
	for _, sheet := range file.GetSheetList() {
		iterator, err := file.Rows(sheet)
		if err != nil {
			return nil, ErrUnreadable
		}
		sheetRow := 0
		for iterator.Next() {
			sheetRow++
			rows++
			if rows > MaxRows {
				iterator.Close()
				return nil, ErrOverLimit
			}
			columns, err := iterator.Columns()
			if err != nil {
				iterator.Close()
				return nil, ErrUnreadable
			}
			for column, value := range columns {
				cells++
				if cells > MaxCells {
					iterator.Close()
					return nil, ErrOverLimit
				}
				address, err := excelize.CoordinatesToCellName(column+1, sheetRow)
				if err != nil {
					iterator.Close()
					return nil, ErrUnreadable
				}
				if value == "" {
					value, _ = file.GetCellFormula(sheet, address)
				}
				if strings.TrimSpace(value) != "" {
					fragments = append(fragments, Fragment{Locator: Locator{Kind: "cell", Value: sheet + "!" + address}, Text: value})
				}
			}
		}
		if err := iterator.Error(); err != nil || iterator.Close() != nil {
			return nil, ErrUnreadable
		}
	}
	return fragments, nil
}

func boundedPDF(ctx context.Context, parser PDFParser, content []byte) ([]Fragment, error) {
	type result struct {
		fragments []Fragment
		err       error
	}
	resultCh := make(chan result, 1)
	parseContext, cancel := context.WithTimeout(ctx, MaxPDFDuration)
	defer cancel()
	go func() {
		defer func() {
			if recover() != nil {
				resultCh <- result{err: ErrParserPanic}
			}
		}()
		fragments, err := parser.Extract(parseContext, content, Limits{MaxPages: MaxPDFPages, MaxText: MaxPDFTextBytes})
		resultCh <- result{fragments: fragments, err: err}
	}()
	select {
	case <-parseContext.Done():
		return nil, ErrParserTimeout
	case result := <-resultCh:
		if result.err != nil {
			return nil, classifyPDFError(result.err)
		}
		var total int
		for _, fragment := range result.fragments {
			if fragment.Locator.Kind != "page" || !strings.HasPrefix(fragment.Locator.Value, "page:") {
				return nil, ErrUnreadable
			}
			total += len(fragment.Text)
			if total > MaxPDFTextBytes {
				return nil, ErrOverLimit
			}
		}
		if len(result.fragments) == 0 {
			return nil, ErrScannedPDF
		}
		return result.fragments, nil
	}
}

func classifyPDFError(err error) error {
	switch {
	case isPDFContextError(err):
		return ErrParserTimeout
	case errors.Is(err, ErrOverLimit), errors.Is(err, ErrScannedPDF), errors.Is(err, ErrEncryptedPDF), errors.Is(err, ErrParserPanic), errors.Is(err, ErrParserTimeout):
		return err
	default:
		return ErrUnreadable
	}
}

func digest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func mappingText(fragments []Fragment) string {
	var builder strings.Builder
	for _, fragment := range fragments {
		line := fragment.Locator.Value + ": " + strings.TrimSpace(fragment.Text) + "\n"
		if builder.Len()+len(line) > MaxMappingText {
			break
		}
		builder.WriteString(line)
	}
	return builder.String()
}

// SourceRef is enough immutable evidence context to prepare domain drafts.
type SourceRef struct {
	ID, Family string
	Version    int64
}

func (s SourceRef) valid() bool {
	return s.ID != "" && s.Version > 0 && (s.Family == "csv" || s.Family == "xlsx" || s.Family == "pdf")
}

func FinanceDraft(source SourceRef, locator Locator, draft finance.Draft) (finance.Draft, error) {
	if !sourced(source, locator) {
		return finance.Draft{}, ErrInvalidInput
	}
	draft.Provenance = finance.Provenance{SourceID: source.ID, SourceFamily: source.Family, SourceVersion: source.Version, LocatorKind: locator.Kind, LocatorValue: locator.Value, GeneratedBy: "ai", SchemaVersion: "import-v1"}
	return draft, nil
}

func ObservationDraft(source SourceRef, locator Locator, draft health.ObservationDraft) (health.ObservationDraft, error) {
	if !sourced(source, locator) {
		return health.ObservationDraft{}, ErrInvalidInput
	}
	draft.Provenance = health.Provenance{SourceID: source.ID, SourceFamily: source.Family, SourceVersion: source.Version, LocatorKind: locator.Kind, LocatorValue: locator.Value, GeneratedBy: "ai", SchemaVersion: "import-v1"}
	return draft, nil
}

func EventDraft(source SourceRef, locator Locator, draft planning.EventDraft) (planning.EventDraft, error) {
	if !sourced(source, locator) {
		return planning.EventDraft{}, ErrInvalidInput
	}
	draft.Provenance = planning.Provenance{SourceID: source.ID, SourceFamily: source.Family, SourceVersion: source.Version, LocatorKind: locator.Kind, LocatorValue: locator.Value, GeneratedBy: "ai", SchemaVersion: "import-v1"}
	return draft, nil
}

func sourced(source SourceRef, locator Locator) bool {
	return source.valid() && locator.Kind != "" && locator.Value != ""
}

// ExactDuplicateKey scopes a digest to precisely the only records it may suppress.
func ExactDuplicateKey(actor policy.ActorScope, visibility policy.Visibility, sourceDigest string) (string, error) {
	if !actor.Valid() || (visibility != policy.Personal && visibility != policy.Shared) || len(sourceDigest) != sha256.Size*2 {
		return "", ErrInvalidInput
	}
	if _, err := hex.DecodeString(sourceDigest); err != nil {
		return "", ErrInvalidInput
	}
	return actor.HouseholdID + "\x00" + actor.ActorID + "\x00" + string(visibility) + "\x00" + strings.ToLower(sourceDigest), nil
}

// SuccessorToken requires the caller to explicitly match a changed upload to a prior source.
type SuccessorToken struct{ PriorSourceID, PriorDigest, ScopeKey string }

func NewSuccessorToken(priorSourceID, priorDigest, scopeKey string) (SuccessorToken, error) {
	if priorSourceID == "" || scopeKey == "" || len(priorDigest) != sha256.Size*2 {
		return SuccessorToken{}, ErrInvalidInput
	}
	if _, err := hex.DecodeString(priorDigest); err != nil {
		return SuccessorToken{}, ErrInvalidInput
	}
	return SuccessorToken{PriorSourceID: priorSourceID, PriorDigest: strings.ToLower(priorDigest), ScopeKey: scopeKey}, nil
}

func (t SuccessorToken) Matches(scopeKey, digest string) bool {
	if t.ScopeKey != scopeKey || len(digest) != sha256.Size*2 {
		return false
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return false
	}
	return !strings.EqualFold(t.PriorDigest, digest)
}
