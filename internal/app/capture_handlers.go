package app

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/glnarayanan/mithra/internal/capture"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/providers"
)

const (
	maxCaptureTextBytes = 12_000
	maxCaptureAudio     = 8 << 20
	maxCaptureSeconds   = 90
)

type CaptureView struct {
	Navigation         []NavigationItem
	CSRF               string
	Status             string
	Error              string
	ProviderConfigured bool
	VoiceSupported     bool
	Pending            *CaptureItemView
	Recent             []CaptureItemView
}

type CaptureItemView struct {
	ID, Kind, Title, Summary, Clarification, ClarificationField, SourceURL string
	Details                                                                []CaptureDetailView
	Undoable                                                               bool
}

type CaptureDetailView struct{ Label, Value string }

func (a *App) capture(w http.ResponseWriter, r *http.Request) {
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
		status := ""
		if r.URL.Query().Get("discarded") == "1" {
			status = "Capture discarded. No record was kept."
		}
		a.renderCapture(r, w, scope, csrf, status, "")
		return
	}
	if !a.validSessionMutation(r, a.sessionCookie(r)) {
		a.renderCapture(r, w, scope, csrf, "", "We could not verify that request. Nothing was changed.")
		return
	}
	switch r.PostForm.Get("action") {
	case "text":
		a.captureText(r, w, scope, csrf)
	case "answer":
		a.answerCapture(r, w, scope, csrf)
	case "confirm":
		if err := a.captureRecords.Confirm(r.Context(), scope, boundedField(r, "capture_id", 128)); err != nil {
			a.renderCapture(r, w, scope, csrf, "", "That capture is no longer available to confirm.")
			return
		}
		a.renderCapture(r, w, scope, csrf, "Update confirmed. The raw recording has been deleted.", "")
	case "cancel":
		if err := a.captureRecords.Discard(r.Context(), scope, boundedField(r, "capture_id", 128)); err != nil {
			a.renderCapture(r, w, scope, csrf, "", "That capture could not be discarded safely.")
			return
		}
		http.Redirect(w, r, "/capture?discarded=1", http.StatusSeeOther)
	case "undo":
		if err := a.captureRecords.Undo(r.Context(), scope, boundedField(r, "capture_id", 128)); err != nil {
			a.renderCapture(r, w, scope, csrf, "", "Undo is no longer safe because the record changed or the undo window ended.")
			return
		}
		a.renderCapture(r, w, scope, csrf, "The captured record was undone.", "")
	default:
		a.renderCapture(r, w, scope, csrf, "", "Choose an available capture action.")
	}
}

func (a *App) captureText(r *http.Request, w http.ResponseWriter, scope policy.ActorScope, csrf string) {
	text := boundedField(r, "update", maxCaptureTextBytes)
	visibility, ok := captureVisibility(r.PostForm.Get("visibility"))
	if strings.TrimSpace(text) == "" || !ok {
		a.renderCapture(r, w, scope, csrf, "", "Enter an update and choose where Mithra should keep it.")
		return
	}
	summary, proposal, err := a.analyzeCapture(r.Context(), scope, text)
	if err != nil {
		logRequestError(a.logger, r.Context(), "capture_analysis_failed")
		a.renderCapture(r, w, scope, csrf, "", "Mithra could not process that update. Nothing was saved; try again when the AI connection is available.")
		return
	}
	a.completeCaptureFacts(r, scope, &proposal)
	receipt, err := a.captureRecords.SubmitText(r.Context(), scope, capture.TextRequest{Text: text, Summary: summary, Visibility: visibility, Proposal: proposal})
	if err != nil {
		logRequestError(a.logger, r.Context(), "capture_commit_failed")
		a.renderCapture(r, w, scope, csrf, "", "Mithra could not organise that update safely. Nothing was added.")
		return
	}
	if receipt.State == "clarification" {
		a.renderCapture(r, w, scope, csrf, "Mithra needs one detail before it can keep this record.", "")
		return
	}
	if err := a.captureRecords.Confirm(r.Context(), scope, receipt.ID); err != nil {
		logRequestError(a.logger, r.Context(), "capture_confirmation_failed")
		a.renderCapture(r, w, scope, csrf, "", "The update was processed but could not be confirmed. Review it below.")
		return
	}
	http.Redirect(w, r, "/?captured=1#capture", http.StatusSeeOther)
}

func (a *App) answerCapture(r *http.Request, w http.ResponseWriter, scope policy.ActorScope, csrf string) {
	receipt, err := a.captureRecords.AnswerClarification(r.Context(), scope, boundedField(r, "capture_id", 128), boundedField(r, "answer", 256))
	if err != nil {
		a.renderCapture(r, w, scope, csrf, "", "That detail was not valid for this record. Check the value and unit, date, owner, or status.")
		return
	}
	if receipt.State == "clarification" {
		a.renderCapture(r, w, scope, csrf, "One more essential detail is missing.", "")
		return
	}
	if receipt.RawAudioSourceID == "" {
		if err := a.captureRecords.Confirm(r.Context(), scope, receipt.ID); err != nil {
			a.renderCapture(r, w, scope, csrf, "", "The record was processed but could not be confirmed. Review it below.")
			return
		}
		a.renderCapture(r, w, scope, csrf, receipt.Summary+" You can undo this for ten minutes.", "")
		return
	}
	a.renderCapture(r, w, scope, csrf, "Review the transcript and proposed record before keeping it.", "")
}

func (a *App) captureVoice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowedFor(w, "POST")
		return
	}
	scope, _, ok := a.authenticated(r)
	if !ok {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	select {
	case a.captureVoiceSlot <- struct{}{}:
		defer func() { <-a.captureVoiceSlot }()
	default:
		writeError(w, http.StatusServiceUnavailable, "voice capture is busy; try again shortly")
		return
	}
	if err := r.ParseMultipartForm(maxCaptureAudio + (512 << 10)); err != nil || !a.validSessionMutation(r, a.sessionCookie(r)) {
		writeError(w, http.StatusBadRequest, "invalid voice capture")
		return
	}
	visibility, validVisibility := captureVisibility(r.FormValue("visibility"))
	duration, durationErr := strconv.Atoi(r.FormValue("duration_seconds"))
	files := r.MultipartForm.File["audio"]
	if !validVisibility || durationErr != nil || duration < 1 || duration > maxCaptureSeconds || len(files) != 1 || files[0].Size < 1 || files[0].Size > maxCaptureAudio {
		writeError(w, http.StatusBadRequest, "invalid voice capture")
		return
	}
	mediaType, _, err := mime.ParseMediaType(files[0].Header.Get("Content-Type"))
	format := map[string]string{"audio/webm": "webm", "audio/ogg": "ogg", "audio/mp4": "mp4"}[mediaType]
	if err != nil || format == "" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported voice format")
		return
	}
	file, err := files[0].Open()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid voice capture")
		return
	}
	audio, readErr := io.ReadAll(io.LimitReader(file, maxCaptureAudio+1))
	_ = file.Close()
	if readErr != nil || len(audio) < 1 || len(audio) > maxCaptureAudio {
		clear(audio)
		writeError(w, http.StatusBadRequest, "invalid voice capture")
		return
	}
	defer clear(audio)
	if !validCaptureAudio(format, audio) {
		writeError(w, http.StatusUnsupportedMediaType, "invalid voice container")
		return
	}
	receipt, err := a.captureRecords.StageAudio(r.Context(), scope, capture.AudioRequest{Bytes: audio, Visibility: visibility})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "voice recording could not be started")
		return
	}
	client, err := a.modelFor(r.Context(), scope)
	if err != nil {
		_ = a.captureRecords.FailAudio(r.Context(), scope, receipt.ID, true)
		_ = a.captureRecords.Cleanup(r.Context(), time.Now())
		writeError(w, http.StatusServiceUnavailable, "AI connection is unavailable")
		return
	}
	transcript, err := client.Transcribe(r.Context(), format, audio)
	if err != nil {
		if errors.Is(err, providers.ErrUnsupportedOperation) {
			_ = a.captureRecords.FailAudio(r.Context(), scope, receipt.ID, true)
			_ = a.captureRecords.Cleanup(r.Context(), time.Now())
			writeError(w, http.StatusUnprocessableEntity, "Voice updates need OpenAI. Choose OpenAI in Settings before recording.")
			return
		}
		terminal := errors.Is(err, providers.ErrInvalidCredential) || errors.Is(err, providers.ErrInvalidResponse) || errors.Is(err, providers.ErrRefusal)
		_ = a.captureRecords.FailAudio(r.Context(), scope, receipt.ID, terminal)
		if terminal {
			_ = a.captureRecords.Cleanup(r.Context(), time.Now())
		}
		writeError(w, http.StatusServiceUnavailable, "voice transcription is temporarily unavailable")
		return
	}
	summary, proposal, err := a.analyzeCapture(r.Context(), scope, transcript)
	if err != nil {
		_ = a.captureRecords.FailAudio(r.Context(), scope, receipt.ID, false)
		writeError(w, http.StatusServiceUnavailable, "voice update could not be processed")
		return
	}
	a.completeCaptureFacts(r, scope, &proposal)
	if _, err := a.captureRecords.SubmitTranscript(r.Context(), scope, receipt.ID, capture.TextRequest{Text: transcript, Summary: summary, Visibility: visibility, Proposal: proposal}); err != nil {
		_ = a.captureRecords.FailAudio(r.Context(), scope, receipt.ID, true)
		_ = a.captureRecords.Cleanup(r.Context(), time.Now())
		writeError(w, http.StatusBadRequest, "voice update did not pass record checks")
		return
	}
	http.Redirect(w, r, "/capture", http.StatusSeeOther)
}

func validCaptureAudio(format string, audio []byte) bool {
	switch format {
	case "webm":
		return len(audio) >= 4 && audio[0] == 0x1a && audio[1] == 0x45 && audio[2] == 0xdf && audio[3] == 0xa3
	case "ogg":
		return len(audio) >= 4 && string(audio[:4]) == "OggS"
	case "mp4":
		return len(audio) >= 12 && string(audio[4:8]) == "ftyp"
	default:
		return false
	}
}

func (a *App) modelFor(ctx context.Context, scope policy.ActorScope) (*providers.ModelClient, error) {
	config, err := a.providerSettings.ProviderConfig(ctx, scope)
	if err != nil {
		return nil, err
	}
	client, err := providers.NewModelClient(providers.ModelClientConfig{ModelConfig: providers.ModelConfig{ProviderID: config.ProviderID, Model: config.Model, BaseURL: config.BaseURL, APIKey: config.APIKey}, Client: a.openAIClient})
	config.APIKey = ""
	return client, err
}

func (a *App) renderCapture(r *http.Request, w http.ResponseWriter, scope policy.ActorScope, csrf, status, problem string) {
	receipts, err := a.captureRecords.List(r.Context(), scope, 20)
	if err != nil {
		logRequestError(a.logger, r.Context(), "capture_list_failed")
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	config, providerErr := a.providerSettings.ProviderDetails(r.Context(), scope)
	configured := providerErr == nil
	view := CaptureView{Navigation: navigationForPath("/capture"), CSRF: csrf, Status: status, Error: problem, ProviderConfigured: configured, VoiceSupported: configured && config.ProviderID == providers.ProviderOpenAI}
	for _, receipt := range receipts {
		item := captureItemView(receipt, time.Now())
		if view.Pending == nil && (receipt.State == "clarification" || receipt.State == "awaiting_confirmation") {
			view.Pending = &item
			continue
		}
		if receipt.State == "confirmed" || receipt.State == "undone" {
			view.Recent = append(view.Recent, item)
		}
	}
	a.renderTemplate(r.Context(), w, "capture.html", view)
}

func captureItemView(receipt capture.Capture, now time.Time) CaptureItemView {
	kind := strings.Title(receipt.RecordFamily)
	if kind == "" {
		kind = "Update"
	}
	title := receipt.Summary
	if title == "" {
		title = "Captured update"
	}
	item := CaptureItemView{ID: receipt.ID, Kind: kind, Title: title, Summary: receipt.Summary, Clarification: receipt.ClarificationQuestion, ClarificationField: receipt.ClarificationField, Undoable: receipt.State == "confirmed" && receipt.RecordID != "" && receipt.UndoUntil.After(now)}
	if receipt.SourceID != "" {
		item.SourceURL = sourceURL(receipt.SourceID)
	}
	item.Details = []CaptureDetailView{{Label: "Record", Value: kind}, {Label: "Visibility", Value: map[policy.Visibility]string{policy.Personal: "Only you", policy.Shared: "Shared"}[receipt.Visibility]}}
	return item
}

func captureVisibility(value string) (policy.Visibility, bool) {
	v := policy.Visibility(value)
	return v, v == policy.Personal || v == policy.Shared
}

func (a *App) completeCaptureFacts(r *http.Request, scope policy.ActorScope, proposal *capture.Proposal) {
	if proposal == nil || proposal.Planning == nil || proposal.Planning.AllDay || proposal.Planning.Timezone != "" {
		return
	}
	if zone, err := a.planningRecords.GetTimezone(r.Context(), scope); err == nil {
		proposal.Planning.Timezone = zone
	}
}

func boundedField(r *http.Request, name string, limit int) string {
	value := r.FormValue(name)
	if len(value) > limit {
		return ""
	}
	return strings.TrimSpace(value)
}
