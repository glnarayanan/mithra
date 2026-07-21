package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestOpenAIStructuredUsesPrivacyContractAndParsesReasoningItems(t *testing.T) {
	var captured map[string]any
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != responsesEndpoint || request.Header.Get("Authorization") != "Bearer sk-test-secret" || request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("request = %s %#v", request.URL, request.Header)
		}
		if err := json.NewDecoder(request.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		return providerResponse(request, http.StatusOK, `{"status":"completed","output":[{"type":"reasoning","summary":[]},{"type":"message","content":[{"type":"output_text","text":"{\"value\":7}"}]}]}`), nil
	})}
	provider, err := NewOpenAI(OpenAIConfig{APIKey: "sk-test-secret", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Structured(context.Background(), structuredFixture())
	if err != nil || string(result) != `{"value":7}` {
		t.Fatalf("result = %s, %v", result, err)
	}
	if stored, ok := captured["store"].(bool); !ok || stored {
		t.Fatalf("store = %#v", captured["store"])
	}
	if captured["model"] != ResponsesModel || captured["input"] != "Only permitted source text" {
		t.Fatalf("request payload = %#v", captured)
	}
	text, ok := captured["text"].(map[string]any)
	if !ok {
		t.Fatalf("text format = %#v", captured["text"])
	}
	format, ok := text["format"].(map[string]any)
	if !ok || format["type"] != "json_schema" || format["strict"] != true || format["name"] != "mithra_fact" {
		t.Fatalf("schema format = %#v", text)
	}
}

func TestOpenAIStructuredClassifiesFailuresWithoutReflectingBodies(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"refusal", 200, `{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"sensitive upstream reason"}]}]}`, ErrRefusal},
		{"incomplete", 200, `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`, ErrIncomplete},
		{"malformed", 200, `{`, ErrInvalidResponse},
		{"invalid output", 200, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"not json"}]}]}`, ErrInvalidResponse},
		{"ambiguous output", 200, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"value\":1}"},{"type":"output_text","text":"{\"value\":2}"}]}]}`, ErrInvalidResponse},
		{"unauthorized", 401, `{"error":{"message":"secret reflected body"}}`, ErrInvalidCredential},
		{"rate limited", 429, `{"error":{"message":"secret reflected body"}}`, ErrRateLimited},
		{"server", 500, `{"error":{"message":"secret reflected body"}}`, ErrProviderUnavailable},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return providerResponse(request, test.status, test.body), nil
			})}
			provider, err := NewOpenAI(OpenAIConfig{APIKey: "sk-sensitive", Client: client})
			if err != nil {
				t.Fatal(err)
			}
			_, err = provider.Structured(context.Background(), structuredFixture())
			if !errors.Is(err, test.want) || strings.Contains(err.Error(), "secret reflected") || strings.Contains(err.Error(), "sensitive upstream") {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestOpenAIRejectsRedirectOversizeAndTimeout(t *testing.T) {
	redirectClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		response := providerResponse(request, http.StatusFound, "redirect")
		response.Header.Set("Location", "https://evil.example/collect")
		return response, nil
	})}
	provider, err := NewOpenAI(OpenAIConfig{APIKey: "sk-test", Client: redirectClient})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Structured(context.Background(), structuredFixture()); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("redirect error = %v", err)
	}

	oversizeClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return providerResponse(request, http.StatusOK, strings.Repeat("x", maxProviderResponse+1)), nil
	})}
	provider, _ = NewOpenAI(OpenAIConfig{APIKey: "sk-test", Client: oversizeClient})
	if _, err := provider.Structured(context.Background(), structuredFixture()); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("oversize error = %v", err)
	}

	timeoutClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	provider, _ = NewOpenAI(OpenAIConfig{APIKey: "sk-test", Client: timeoutClient, Timeout: 10 * time.Millisecond})
	if _, err := provider.Structured(context.Background(), structuredFixture()); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestOpenAITranscriptionUsesFixedEndpointAndBoundedMultipart(t *testing.T) {
	audio := []byte("RIFF-not-real-audio")
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != transcriptionEndpoint || !strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data;") {
			t.Fatalf("transcription request = %s %q", request.URL, request.Header.Get("Content-Type"))
		}
		if err := request.ParseMultipartForm(maxAudioBytes + 1024); err != nil {
			t.Fatal(err)
		}
		file, header, err := request.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		content, _ := io.ReadAll(file)
		if header.Filename != "audio.wav" || !bytes.Equal(content, audio) || request.FormValue("model") != TranscriptionModel || request.FormValue("response_format") != "json" {
			t.Fatalf("multipart = %q %q %q %q", header.Filename, content, request.FormValue("model"), request.FormValue("response_format"))
		}
		return providerResponse(request, http.StatusOK, `{"text":"Dinner is on Tuesday."}`), nil
	})}
	provider, err := NewOpenAI(OpenAIConfig{APIKey: "sk-test", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	text, err := provider.Transcribe(context.Background(), "wav", audio)
	if err != nil || text != "Dinner is on Tuesday." {
		t.Fatalf("transcription = %q, %v", text, err)
	}
	if _, err := provider.Transcribe(context.Background(), "exe", audio); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("invalid format = %v", err)
	}
	if _, err := provider.Transcribe(context.Background(), "wav", make([]byte, maxAudioBytes+1)); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("oversized audio = %v", err)
	}
}

func structuredFixture() StructuredRequest {
	return StructuredRequest{
		Instructions:    "Extract only supported facts.",
		Input:           "Only permitted source text",
		SchemaName:      "mithra_fact",
		Schema:          json.RawMessage(`{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"],"additionalProperties":false}`),
		MaxOutputTokens: 128,
	}
}

func providerResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}
