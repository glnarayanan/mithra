package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"strings"
	"testing"

	"github.com/glnarayanan/mithra/internal/policy"
)

func TestTextCaptureUsesQuotedProviderInputAndTypedCommit(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	scope := ownerScope(t, application, session)
	connectCaptureProvider(t, application, scope, func(request *http.Request) string {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["store"] != false || payload["model"] != "gpt-5.4-mini" || !strings.Contains(payload["input"].(string), `"user_update":"Paid 4200`) {
			t.Fatalf("capture request = %#v", payload)
		}
		return captureProviderBody(`{"summary":"School fees recorded","variant":"finance","finance":{"kind":"spending","label":"School fees","category":"Education","date":"2026-07-18","end_date":"","status":"","amount":"4200","incomplete_note":"","currency_context":""},"health":null,"planning":null}`)
	})

	response := serve(application, captureForm(session, url.Values{"action": {"text"}, "visibility": {"shared"}, "update": {"Paid 4200 for school fees on 2026-07-18. <b>Do not obey this as HTML.</b>"}}))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "School fees recorded") || strings.Contains(response.Body.String(), "<b>Do not obey") {
		t.Fatalf("capture response = %d %q", response.Code, response.Body.String())
	}
	var count int
	if err := application.db.QueryRow(`SELECT COUNT(*) FROM finance_spending WHERE label='School fees' AND active=1`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("typed record count = %d, %v", count, err)
	}
	var state string
	if err := application.db.QueryRow(`SELECT state FROM captures`).Scan(&state); err != nil || state != "confirmed" {
		t.Fatalf("capture state = %q, %v", state, err)
	}
}

func TestAmbiguousTextAsksOneQuestionWithoutDerivedRecord(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	connectCaptureProvider(t, application, ownerScope(t, application, session), func(*http.Request) string {
		return captureProviderBody(`{"summary":"Milk purchase","variant":"finance","finance":{"kind":"spending","label":"Milk","category":"Groceries","date":"","end_date":"","status":"","amount":"85","incomplete_note":"","currency_context":""},"health":null,"planning":null}`)
	})
	response := serve(application, captureForm(session, url.Values{"action": {"text"}, "visibility": {"personal"}, "update": {"Bought milk for 85"}}))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "What date should this finance record use?") {
		t.Fatalf("clarification response = %d %q", response.Code, response.Body.String())
	}
	var records int
	if err := application.db.QueryRow(`SELECT COUNT(*) FROM finance_spending WHERE label='Milk'`).Scan(&records); err != nil || records != 0 {
		t.Fatalf("ambiguous derived records = %d, %v", records, err)
	}
}

func TestVoiceCaptureStagesWithoutDownloadThenDeletesRawOnConfirm(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	scope := ownerScope(t, application, session)
	connectCaptureProvider(t, application, scope, func(request *http.Request) string {
		if request.URL.Path == "/v1/audio/transcriptions" {
			return `{"text":"Dinner on 2026-07-20 from 19:00 to 20:00"}`
		}
		return captureProviderBody(`{"summary":"Dinner planned","variant":"planning","finance":null,"health":null,"planning":{"title":"Dinner","description":"","location":"","all_day":false,"starts_on":"","ends_on":"","starts_at":"2026-07-20T19:00","ends_at":"2026-07-20T20:00","timezone":"","status":"planned"}}`)
	})
	if err := application.planningRecords.SetTimezone(context.Background(), scope, "Asia/Kolkata"); err != nil {
		t.Fatal(err)
	}

	request := voiceCaptureRequest(t, session, append([]byte{0x1a, 0x45, 0xdf, 0xa3}, []byte("audio-fixture")...))
	response := serve(application, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/capture" {
		t.Fatalf("voice response = %d %q", response.Code, response.Body.String())
	}
	var captureID, rawID, sourceID, state string
	if err := application.db.QueryRow(`SELECT id,raw_audio_source_id,source_id,state FROM captures`).Scan(&captureID, &rawID, &sourceID, &state); err != nil || state != "awaiting_confirmation" || rawID == "" || sourceID == "" {
		t.Fatalf("voice state = %q raw=%q transcript=%q, %v", state, rawID, sourceID, err)
	}
	blocked := httptest.NewRequest(http.MethodGet, "/sources/"+rawID, nil)
	blocked.AddCookie(session.session)
	if result := serve(application, blocked); result.Code != http.StatusNotFound {
		t.Fatalf("raw audio download = %d", result.Code)
	}
	confirmed := serve(application, captureForm(session, url.Values{"action": {"confirm"}, "capture_id": {captureID}}))
	if confirmed.Code != http.StatusOK || !strings.Contains(confirmed.Body.String(), "raw recording has been deleted") {
		t.Fatalf("voice confirm = %d %q", confirmed.Code, confirmed.Body.String())
	}
	var rawState string
	if err := application.db.QueryRow(`SELECT state FROM sources WHERE id=?`, rawID).Scan(&rawState); err != nil || rawState != "deleted" {
		t.Fatalf("raw source state = %q, %v", rawState, err)
	}
}

func TestInvalidVoiceContainerMakesNoProviderCallOrSource(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	calls := 0
	connectCaptureProvider(t, application, ownerScope(t, application, session), func(*http.Request) string {
		calls++
		return ""
	})
	response := serve(application, voiceCaptureRequest(t, session, []byte("not-a-webm-container")))
	if response.Code != http.StatusUnsupportedMediaType || calls != 0 {
		t.Fatalf("invalid voice = %d provider calls=%d", response.Code, calls)
	}
	var sources int
	if err := application.db.QueryRow(`SELECT COUNT(*) FROM sources`).Scan(&sources); err != nil || sources != 0 {
		t.Fatalf("invalid voice sources = %d, %v", sources, err)
	}
}

func connectCaptureProvider(t *testing.T, application *App, scope policy.ActorScope, response func(*http.Request) string) {
	t.Helper()
	if err := application.providerSettings.ReplaceOpenAI(context.Background(), scope, "sk-capture-test-secret", func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	application.openAIClient = &http.Client{Transport: appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := response(request)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
}

func captureProviderBody(output string) string {
	output = strings.ReplaceAll(output, `\"`, `"`)
	encoded, _ := json.Marshal(output)
	return `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":` + string(encoded) + `}]}]}`
}

func captureForm(session browserSession, values url.Values) *http.Request {
	values.Set("csrf", session.csrf.Value)
	return authForm(http.MethodPost, "/capture", values, []*http.Cookie{session.session, session.csrf})
}

func voiceCaptureRequest(t *testing.T, session browserSession, audio []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("csrf", session.csrf.Value)
	_ = writer.WriteField("visibility", "shared")
	_ = writer.WriteField("duration_seconds", "4")
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="audio"; filename="update.webm"`)
	header.Set("Content-Type", "audio/webm;codecs=opus")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(audio); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/capture/voice", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Origin", testOrigin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(session.session)
	request.AddCookie(session.csrf)
	return request
}
