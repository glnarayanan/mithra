package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/glnarayanan/mithra/internal/imports"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/providers"
	"github.com/glnarayanan/mithra/internal/storage"
)

type ImportsView struct {
	Navigation         []NavigationItem
	CSRF               string
	Status             string
	Error              string
	ErrorCode          string
	ErrorReference     string
	ProviderConfigured bool
	Review             *ImportReviewView
	VisualConsent      *ImportVisualConsentView
	Deletion           *ImportDeletionView
	Recent             []ImportRecentView
	Replacement        *ImportReplacementView
}
type ImportReviewView struct {
	ID, FileName, Summary string
	Version               int64
	BlockingRecords       int
	Records               []ImportRecordView
	Blockers, Warnings    []ImportIssueView
}
type ImportRecordView struct {
	Family, Title, Locator, Change string
	Fields                         []ImportFieldView
}
type ImportFieldView struct {
	ID, Label, Value, Error, Type string
	Required, Invalid             bool
	Options                       []ImportOptionView
}
type ImportOptionView struct {
	Value, Label string
	Selected     bool
}
type ImportIssueView struct{ FieldID, Message, Locator string }
type ImportRecentView struct {
	ID, FileName, Summary, Visibility, SourceURL string
	CanReplace                                   bool
}
type ImportVisualConsentView struct {
	ID, FileName, Token string
	Version             int64
	Expires             string
}
type ImportDeletionView struct {
	ID, FileName, Token, Visibility string
	Records, Jobs                   int
	Expires                         string
}
type ImportReplacementView struct{ ID, FileName, Visibility string }

func (a *App) importDocuments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
		methodNotAllowedFor(w, "GET, HEAD, POST")
		return
	}
	scope, csrf, ok := a.authenticated(r)
	if !ok {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodHead {
		writeHTMLHead(w)
		return
	}
	if r.Method == http.MethodGet {
		a.renderImports(r, w, scope, csrf, "", "")
		return
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		a.uploadImport(r, w, scope, csrf)
		return
	}
	if !a.validSessionMutation(r, a.sessionCookie(r)) {
		a.renderImports(r, w, scope, csrf, "", "We could not verify that request. Nothing was changed.")
		return
	}
	switch r.PostForm.Get("action") {
	case "correct":
		a.correctImport(r, w, scope, csrf)
	case "visual_confirm":
		a.confirmVisualImport(r, w, scope, csrf)
	case "prepare_delete":
		impact, err := a.imports.PrepareDeletion(r.Context(), scope, boundedField(r, "import_id", 64))
		if err != nil {
			a.renderImports(r, w, scope, csrf, "", "That import is no longer available to delete.")
			return
		}
		a.renderImportDeletion(r, w, scope, csrf, impact, "Review exactly what will be removed.", "")
	case "delete_confirm":
		if err := a.imports.ConfirmDeletion(r.Context(), scope, boundedField(r, "import_id", 64), boundedField(r, "deletion_token", 128)); err != nil {
			if errors.Is(err, imports.ErrCleanupPending) {
				a.renderImports(r, w, scope, csrf, "The source and its records are no longer available. Mithra will finish removing the encrypted file automatically.", "")
				return
			}
			a.renderImports(r, w, scope, csrf, "", "That deletion confirmation expired or changed. Nothing was removed.")
			return
		}
		a.renderImports(r, w, scope, csrf, "Source deleted. Its records and pending work are no longer accessible.", "")
	case "commit":
		id := boundedField(r, "import_id", 64)
		version, _ := strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
		review, err := a.imports.Get(r.Context(), scope, id)
		if err != nil || review.Version != version {
			a.renderImports(r, w, scope, csrf, "", "That import review changed. Open it again before importing.")
			return
		}
		if submittedImportFields(r) {
			applyImportFields(r, &review.Proposals)
		}
		updated, err := a.imports.Correct(r.Context(), scope, id, version, review.Proposals)
		if err != nil {
			a.renderImportReview(r, w, scope, csrf, id, "", "Those corrections could not be saved safely.")
			return
		}
		if blockingIssues(updated.Issues) != 0 {
			a.renderImportReview(r, w, scope, csrf, id, "", "Some highlighted values still need your attention.")
			return
		}
		if err := a.imports.Commit(r.Context(), scope, id, updated.Version); err != nil {
			a.renderImportReview(r, w, scope, csrf, id, "", "The review changed, a required value is unresolved, or household data changed. Check the highlighted fields again.")
			return
		}
		a.renderImports(r, w, scope, csrf, "Import complete. Every record remains linked to its source.", "")
	case "discard":
		if err := a.imports.Discard(r.Context(), scope, boundedField(r, "import_id", 64)); err != nil {
			a.renderImports(r, w, scope, csrf, "", "That import is no longer available to discard.")
			return
		}
		a.renderImports(r, w, scope, csrf, "Import discarded. No records or source file were kept.", "")
	default:
		a.renderImports(r, w, scope, csrf, "", "Choose an available import action.")
	}
}

func (a *App) uploadImport(r *http.Request, w http.ResponseWriter, scope policy.ActorScope, csrf string) {
	if err := r.ParseMultipartForm(imports.MaxFileBytes + (512 << 10)); err != nil || !a.validSessionMutation(r, a.sessionCookie(r)) {
		a.renderImports(r, w, scope, csrf, "", "Choose one CSV, XLSX, or PDF file up to 10 MB.")
		return
	}
	visibility, validVisibility := captureVisibility(r.FormValue("visibility"))
	files := r.MultipartForm.File["file"]
	if !validVisibility || len(r.MultipartForm.File) != 1 || len(files) != 1 || files[0].Size < 1 || files[0].Size > imports.MaxFileBytes {
		a.renderImports(r, w, scope, csrf, "", "Choose exactly one CSV, XLSX, or PDF file up to 10 MB.")
		return
	}
	file, err := files[0].Open()
	if err != nil {
		a.renderImports(r, w, scope, csrf, "", "That file could not be read safely.")
		return
	}
	content, readErr := io.ReadAll(io.LimitReader(file, imports.MaxFileBytes+1))
	_ = file.Close()
	if readErr != nil || len(content) < 1 || len(content) > imports.MaxFileBytes {
		clear(content)
		a.renderImports(r, w, scope, csrf, "", "That file could not be read safely.")
		return
	}
	defer clear(content)
	mediaType, _, _ := mime.ParseMediaType(files[0].Header.Get("Content-Type"))
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	document, err := a.importExtractor.Extract(ctx, imports.Input{Name: filepath.Base(files[0].Filename), ContentType: mediaType, Bytes: content})
	if err != nil {
		if errors.Is(err, imports.ErrScannedPDF) {
			if a.imports.ExactExists(r.Context(), scope, visibility, documentDigest(content)) {
				a.renderImports(r, w, scope, csrf, "", "Mithra already has this file with the same privacy setting. Nothing was copied or sent.")
				return
			}
			source, storeErr := a.sources.Store(r.Context(), scope, content, storage.Metadata{Family: "pdf", Version: 1, Visibility: visibility, LocatorKind: "source", LocatorValue: "document"})
			if storeErr != nil {
				a.renderImports(r, w, scope, csrf, "", "Mithra could not prepare the encrypted PDF. Nothing was sent. Try uploading it again.")
				return
			}
			consent, consentErr := a.imports.StageVisualConsent(r.Context(), scope, source, filepath.Base(files[0].Filename))
			if consentErr != nil {
				_ = a.sources.Delete(r.Context(), scope, source.ID)
				a.renderImports(r, w, scope, csrf, "", "The visual-PDF confirmation could not be prepared safely. Nothing was sent.")
				return
			}
			a.renderVisualConsent(r, w, scope, csrf, consent, "This PDF has no locally readable text. Review the transfer details before deciding.", "")
			return
		}
		code, message := importExtractionFailure(err, files[0].Filename)
		logRequestError(a.logger, r.Context(), code)
		w.WriteHeader(http.StatusUnprocessableEntity)
		a.renderImports(r, w, scope, csrf, "", message)
		return
	}
	if a.imports.ExactExists(r.Context(), scope, visibility, document.Digest) {
		a.renderImports(r, w, scope, csrf, "", "Mithra already has this file with the same privacy setting. Nothing was copied or sent.")
		return
	}
	source, err := a.sources.Store(r.Context(), scope, content, storage.Metadata{Family: string(document.Kind), Version: 1, Visibility: visibility, LocatorKind: "source", LocatorValue: "document"})
	if err != nil {
		a.renderImports(r, w, scope, csrf, "", "Mithra could not prepare the encrypted file. Nothing was sent. Try uploading it again.")
		return
	}
	proposals, err := a.analyzeImport(r.Context(), scope, document)
	if err != nil {
		_ = a.sources.Delete(r.Context(), scope, source.ID)
		a.renderImportAnalysisFailure(r, w, scope, csrf, err)
		return
	}
	review, err := a.imports.Stage(r.Context(), scope, source, filepath.Base(files[0].Filename), proposals, boundedField(r, "replaces_import_id", 64))
	if err != nil {
		_ = a.sources.Delete(r.Context(), scope, source.ID)
		if errors.Is(err, imports.ErrDuplicate) {
			a.renderImports(r, w, scope, csrf, "", "Mithra already has this file with the same privacy setting. Nothing was copied.")
			return
		}
		a.renderImports(r, w, scope, csrf, "", "Mithra could not prepare this review. Nothing was imported. Try uploading the file again.")
		return
	}
	a.renderImportReview(r, w, scope, csrf, review.ID, "Your review is ready. Check every highlighted value before importing.", "")
}

func (a *App) confirmVisualImport(r *http.Request, w http.ResponseWriter, scope policy.ActorScope, csrf string) {
	id := boundedField(r, "import_id", 64)
	token := boundedField(r, "consent_token", 128)
	version, _ := strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
	consent, err := a.imports.GetVisualConsent(r.Context(), scope, id)
	if err != nil || consent.Version != version {
		a.renderImports(r, w, scope, csrf, "", "That visual-PDF confirmation expired or changed. Nothing was sent.")
		return
	}
	pdf, source, err := a.sources.Read(r.Context(), scope, consent.SourceID)
	if err != nil || source.Family != "pdf" {
		clear(pdf)
		a.renderImports(r, w, scope, csrf, "", "That PDF is no longer available. Nothing was sent.")
		return
	}
	defer clear(pdf)
	if err := a.imports.ConsumeVisualConsent(r.Context(), scope, id, token, version); err != nil {
		a.renderImports(r, w, scope, csrf, "", "That visual-PDF confirmation expired or changed. Nothing was sent.")
		return
	}
	proposals, err := a.analyzeVisualPDF(r.Context(), scope, pdf)
	if err != nil {
		_ = a.imports.AbortVisual(r.Context(), scope, id, version+1)
		a.renderImportAnalysisFailure(r, w, scope, csrf, err)
		return
	}
	review, err := a.imports.FinishVisual(r.Context(), scope, id, version+1, proposals)
	if err != nil {
		_ = a.imports.AbortVisual(r.Context(), scope, id, version+1)
		a.renderImports(r, w, scope, csrf, "", "Mithra could not prepare this PDF review. Nothing was imported. Upload the file again to retry.")
		return
	}
	a.renderImportReview(r, w, scope, csrf, review.ID, "Your PDF review is ready. Check every highlighted value before importing.", "")
}

func (a *App) correctImport(r *http.Request, w http.ResponseWriter, scope policy.ActorScope, csrf string) {
	id := boundedField(r, "import_id", 64)
	version, _ := strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
	review, err := a.imports.Get(r.Context(), scope, id)
	if err != nil || review.Version != version {
		a.renderImports(r, w, scope, csrf, "", "That import review changed. Open it again before correcting values.")
		return
	}
	applyImportFields(r, &review.Proposals)
	updated, err := a.imports.Correct(r.Context(), scope, id, version, review.Proposals)
	if err != nil {
		a.renderImports(r, w, scope, csrf, "", "Those corrections could not be saved safely.")
		return
	}
	status := "Corrections saved."
	if blockingIssues(updated.Issues) == 0 {
		status = "Corrections saved. This file is ready to import."
	}
	a.renderImportReview(r, w, scope, csrf, id, status, "")
}

func (a *App) renderImports(r *http.Request, w http.ResponseWriter, scope policy.ActorScope, csrf, status, problem string) {
	view := a.importsView(r, scope, csrf, status, problem)
	a.renderTemplate(r.Context(), w, "imports.html", view)
}

func (a *App) importsView(r *http.Request, scope policy.ActorScope, csrf, status, problem string) ImportsView {
	configured, _ := a.providerSettings.Configured(r.Context(), scope)
	recent, _ := a.imports.ListRecent(r.Context(), scope, 10)
	view := ImportsView{Navigation: navigationForPath("/imports"), CSRF: csrf, Status: status, Error: problem, ProviderConfigured: configured}
	if review, err := a.imports.CurrentReview(r.Context(), scope); err == nil {
		item := importReviewView(review)
		view.Review = &item
		return view
	}
	if replacementID := strings.TrimSpace(r.URL.Query().Get("replace")); replacementID != "" {
		if prior, replaceErr := a.imports.Replacement(r.Context(), scope, replacementID); replaceErr == nil {
			view.Replacement = &ImportReplacementView{ID: prior.ID, FileName: prior.FileName, Visibility: map[policy.Visibility]string{policy.Personal: "Only you", policy.Shared: "Shared"}[prior.Visibility]}
		}
	}
	for _, item := range recent {
		summary := fmt.Sprintf("%d records · Source kept", item.Records)
		if item.State == "superseded" {
			summary += " · Prior version"
		}
		view.Recent = append(view.Recent, ImportRecentView{ID: item.ID, FileName: item.FileName, Summary: summary, Visibility: map[policy.Visibility]string{policy.Personal: "Only you", policy.Shared: "Shared"}[item.Visibility], SourceURL: sourceURL(item.SourceID), CanReplace: item.State == "committed"})
	}
	return view
}

func (a *App) renderImportAnalysisFailure(r *http.Request, w http.ResponseWriter, scope policy.ActorScope, csrf string, err error) {
	code, message := importAnalysisFailure(err)
	reference := requestIDFromContext(r.Context())
	logRequestError(a.logger, r.Context(), code)
	w.Header().Set("X-Mithra-Error-Code", code)
	w.WriteHeader(http.StatusBadGateway)
	view := a.importsView(r, scope, csrf, "", message+" The uploaded copy was deleted. Reference: "+reference+".")
	view.ErrorCode = code
	view.ErrorReference = reference
	a.renderTemplate(r.Context(), w, "imports.html", view)
}
func (a *App) renderImportReview(r *http.Request, w http.ResponseWriter, scope policy.ActorScope, csrf, id, status, problem string) {
	review, err := a.imports.Get(r.Context(), scope, id)
	if err != nil {
		a.renderImports(r, w, scope, csrf, "", "That import review is no longer available.")
		return
	}
	configured, _ := a.providerSettings.Configured(r.Context(), scope)
	view := ImportsView{Navigation: navigationForPath("/imports"), CSRF: csrf, Status: status, Error: problem, ProviderConfigured: configured}
	item := importReviewView(review)
	view.Review = &item
	a.renderTemplate(r.Context(), w, "imports.html", view)
}
func (a *App) renderVisualConsent(r *http.Request, w http.ResponseWriter, scope policy.ActorScope, csrf string, consent imports.VisualConsent, status, problem string) {
	configured, _ := a.providerSettings.Configured(r.Context(), scope)
	view := ImportsView{Navigation: navigationForPath("/imports"), CSRF: csrf, Status: status, Error: problem, ProviderConfigured: configured, VisualConsent: &ImportVisualConsentView{ID: consent.ID, FileName: consent.FileName, Token: consent.Token, Version: consent.Version, Expires: consent.ExpiresAt.Local().Format("15:04")}}
	a.renderTemplate(r.Context(), w, "imports.html", view)
}
func (a *App) renderImportDeletion(r *http.Request, w http.ResponseWriter, scope policy.ActorScope, csrf string, impact imports.DeletionImpact, status, problem string) {
	configured, _ := a.providerSettings.Configured(r.Context(), scope)
	view := ImportsView{Navigation: navigationForPath("/imports"), CSRF: csrf, Status: status, Error: problem, ProviderConfigured: configured, Deletion: &ImportDeletionView{ID: impact.ImportID, FileName: impact.FileName, Token: impact.Token, Visibility: map[policy.Visibility]string{policy.Personal: "Only you", policy.Shared: "Shared"}[impact.Visibility], Records: impact.Records, Jobs: impact.Jobs, Expires: impact.ExpiresAt.Local().Format("15:04")}}
	a.renderTemplate(r.Context(), w, "imports.html", view)
}

func importReviewView(review imports.Review) ImportReviewView {
	out := ImportReviewView{ID: review.ID, FileName: review.FileName, Version: review.Version, Summary: fmt.Sprintf("%d entries to review · %s", len(review.Proposals.Records), map[policy.Visibility]string{policy.Personal: "Only you", policy.Shared: "Shared"}[review.Visibility])}
	for i, p := range review.Proposals.Records {
		out.Records = append(out.Records, importRecordView(i, p))
	}
	blockingRecords := map[int]struct{}{}
	for _, issue := range review.Issues {
		item := ImportIssueView{FieldID: fmt.Sprintf("%d-%s", issue.Record, issue.Field), Message: issue.Message, Locator: issue.Locator}
		if issue.Warning {
			out.Warnings = append(out.Warnings, item)
		} else {
			out.Blockers = append(out.Blockers, item)
			blockingRecords[issue.Record] = struct{}{}
		}
	}
	out.BlockingRecords = len(blockingRecords)
	fieldIssues := make(map[string]string)
	for _, issue := range review.Issues {
		if !issue.Warning {
			fieldIssues[fmt.Sprintf("%d-%s", issue.Record, issue.Field)] = issue.Message
		}
	}
	for recordIndex := range out.Records {
		for fieldIndex := range out.Records[recordIndex].Fields {
			field := &out.Records[recordIndex].Fields[fieldIndex]
			if message := fieldIssues[field.ID]; message != "" {
				field.Invalid = true
				field.Error = message
			}
		}
	}
	return out
}
func importRecordView(index int, p imports.ProposedRecord) ImportRecordView {
	record := ImportRecordView{Family: strings.Title(p.Family), Locator: p.Locator.Value, Change: "New record"}
	add := func(name, label, value string, required bool) {
		fieldType := "text"
		switch name {
		case "date", "end_date", "observed_on", "starts_on", "ends_on":
			fieldType = "date"
		case "starts_at", "ends_at":
			fieldType = "datetime-local"
		}
		field := ImportFieldView{ID: fmt.Sprintf("%d-%s", index, name), Label: label, Value: value, Type: fieldType, Required: required}
		if name == "kind" {
			for _, option := range []ImportOptionView{{Value: "income", Label: "Income"}, {Value: "spending", Label: "Spending"}, {Value: "asset", Label: "Asset"}, {Value: "liability", Label: "Liability"}, {Value: "budget", Label: "Budget"}, {Value: "obligation", Label: "Obligation"}} {
				option.Selected = option.Value == value
				field.Options = append(field.Options, option)
			}
		}
		record.Fields = append(record.Fields, field)
	}
	switch p.Family {
	case "finance":
		if p.Finance != nil {
			record.Title = p.Finance.Label
			add("kind", "Kind", p.Finance.Kind, true)
			add("label", "Label", p.Finance.Label, true)
			add("category", "Category", p.Finance.Category, false)
			add("date", "Date", p.Finance.Date, true)
			if p.Finance.Kind == "budget" {
				add("end_date", "End date", p.Finance.EndDate, true)
			}
			if p.Finance.Kind == "obligation" {
				add("status", "Status", p.Finance.Status, false)
			}
			add("amount", "Number", p.Finance.Amount, true)
		}
	case "health":
		if p.Health != nil {
			record.Title = p.Health.Analyte
			add("subject", "Person", p.Health.Subject, true)
			add("analyte", "Measurement", p.Health.Analyte, true)
			add("observed_on", "Observed on", p.Health.ObservedOn, true)
			add("value", "Reported value", p.Health.Value, true)
			add("unit", "Reported unit", p.Health.Unit, true)
			if p.Health.ReferenceLow != "" {
				add("reference_low", "Reference low", p.Health.ReferenceLow, false)
			}
			if p.Health.ReferenceHigh != "" {
				add("reference_high", "Reference high", p.Health.ReferenceHigh, false)
			}
		}
	case "planning":
		if p.Planning != nil {
			record.Title = p.Planning.Title
			add("title", "Event", p.Planning.Title, true)
			if p.Planning.AllDay {
				add("starts_on", "Date", p.Planning.StartsOn, true)
				add("ends_on", "End date", p.Planning.EndsOn, false)
			} else {
				add("starts_at", "Starts", p.Planning.StartsAt, true)
				add("ends_at", "Ends", p.Planning.EndsAt, true)
				add("timezone", "Timezone", p.Planning.Timezone, true)
			}
			add("status", "Status", p.Planning.Status, false)
		}
	}
	if record.Title == "" {
		record.Title = "Proposed record"
	}
	return record
}

func importExtractionFailure(err error, fileName string) (string, string) {
	switch {
	case errors.Is(err, imports.ErrUnsupported):
		return "import_format_unsupported", "This file's contents do not match a supported CSV, XLSX, or PDF file. Nothing was saved or sent."
	case errors.Is(err, imports.ErrEncryptedPDF):
		return "import_pdf_encrypted", "This PDF is password-protected. Save an unlocked copy, then upload that copy. Nothing was saved or sent."
	case errors.Is(err, imports.ErrOverLimit):
		return "import_safety_limit", "This file exceeds Mithra's safe size, page, row, or text limits. Nothing was saved or sent."
	case errors.Is(err, imports.ErrParserTimeout):
		return "import_pdf_timeout", "Mithra could not finish reading this PDF locally in time. Nothing was saved or sent; try the file once more."
	default:
		if strings.EqualFold(filepath.Ext(fileName), ".pdf") {
			return "import_pdf_unreadable", "Mithra could not read this PDF locally. The file may be damaged or use an unsupported PDF feature. Nothing was saved or sent."
		}
		return "import_file_unreadable", "Mithra could not read this file. It may be damaged or use an unsupported feature. Nothing was saved or sent."
	}
}

func importAnalysisFailure(err error) (string, string) {
	switch {
	case errors.Is(err, providers.ErrInvalidCredential):
		return "import_ai_key_rejected", "OpenAI rejected the saved API key. Reconnect OpenAI in Settings, then try again."
	case errors.Is(err, providers.ErrRateLimited):
		return "import_ai_rate_limited", "OpenAI is temporarily rate-limiting requests. Wait a minute, then try again."
	case errors.Is(err, providers.ErrProviderUnavailable):
		return "import_ai_unavailable", "Mithra could not reach OpenAI. Check the connection and try again."
	case errors.Is(err, providers.ErrRefusal):
		return "import_ai_refused", "OpenAI did not process this file. Try a different file or review its contents."
	case errors.Is(err, providers.ErrIncomplete):
		return "import_ai_incomplete", "OpenAI stopped before the review was complete. Try again."
	case errors.Is(err, providers.ErrInvalidResponse):
		return "import_ai_invalid_response", "OpenAI returned a review Mithra could not validate safely. Try again."
	default:
		return "import_ai_failed", "Mithra could not organise this file because the AI request failed. Try again."
	}
}
func applyImportFields(r *http.Request, set *imports.ProposalSet) {
	for i := range set.Records {
		p := &set.Records[i]
		before, _ := json.Marshal(p)
		field := func(name string) string { return boundedField(r, fmt.Sprintf("field_%d-%s", i, name), 512) }
		switch p.Family {
		case "finance":
			if p.Finance != nil {
				p.Finance.Kind = field("kind")
				p.Finance.Label = field("label")
				p.Finance.Category = field("category")
				p.Finance.Date = field("date")
				if p.Finance.Kind == "budget" {
					p.Finance.EndDate = field("end_date")
				}
				if p.Finance.Kind == "obligation" {
					p.Finance.Status = field("status")
				}
				p.Finance.Amount = field("amount")
			}
		case "health":
			if p.Health != nil {
				p.Health.Subject = field("subject")
				p.Health.Analyte = field("analyte")
				p.Health.ObservedOn = field("observed_on")
				p.Health.Value = field("value")
				p.Health.Unit = field("unit")
				if p.Health.ReferenceLow != "" {
					p.Health.ReferenceLow = field("reference_low")
				}
				if p.Health.ReferenceHigh != "" {
					p.Health.ReferenceHigh = field("reference_high")
				}
			}
		case "planning":
			if p.Planning != nil {
				p.Planning.Title = field("title")
				if p.Planning.AllDay {
					p.Planning.StartsOn = field("starts_on")
					p.Planning.EndsOn = field("ends_on")
				} else {
					p.Planning.StartsAt = field("starts_at")
					p.Planning.EndsAt = field("ends_at")
					p.Planning.Timezone = field("timezone")
				}
				p.Planning.Status = field("status")
			}
		}
		after, _ := json.Marshal(p)
		if !bytes.Equal(before, after) {
			p.GeneratedBy = "user"
		}
	}
}
func submittedImportFields(r *http.Request) bool {
	for name := range r.PostForm {
		if strings.HasPrefix(name, "field_") {
			return true
		}
	}
	return false
}
func blockingIssues(issues []imports.Issue) int {
	n := 0
	for _, issue := range issues {
		if !issue.Warning {
			n++
		}
	}
	return n
}

func documentDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
