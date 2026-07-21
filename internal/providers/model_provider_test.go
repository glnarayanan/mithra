package providers

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

type providerRoundTrip func(*http.Request) (*http.Response, error)

func (fn providerRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestModelProviderRegistryAndURLRules(t *testing.T) {
	if len(ModelProviders()) != 18 {
		t.Fatalf("providers = %d", len(ModelProviders()))
	}
	if _, _, err := NormalizeModelConfig(ModelConfig{ProviderID: "unknown", APIKey: "test-key-123456"}); !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("unknown provider = %v", err)
	}
	for _, raw := range []string{"https://user@example.com", "https://example.com/?q=1", "https://example.com/#x", "ftp://example.com", "http://example.com"} {
		if _, _, err := NormalizeModelConfig(ModelConfig{ProviderID: ProviderCustom, Model: "test", BaseURL: raw}); !errors.Is(err, ErrInvalidProvider) {
			t.Fatalf("unsafe URL %q = %v", raw, err)
		}
	}
	config, provider, err := NormalizeModelConfig(ModelConfig{ProviderID: ProviderOllama, Model: "local-model", BaseURL: "http://127.0.0.1:11434/v1"})
	if err != nil || !provider.KeyOptional || config.Model == "" {
		t.Fatalf("local provider = %#v %#v %v", config, provider, err)
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "100.64.0.1", "169.254.169.254", "::1", "fd00::1"} {
		if isPublicProviderIP(net.ParseIP(ip)) {
			t.Fatalf("private provider IP accepted: %s", ip)
		}
	}
	if !isPublicProviderIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public provider IP rejected")
	}
}

func TestModelClientStructuredRequestShapes(t *testing.T) {
	cases := []struct {
		id, endpoint, body string
		header             string
	}{
		{ProviderOpenAI, "/v1/responses", `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"ok\":true}"}]}]}`, "Authorization"},
		{ProviderOpenRouter, "/api/v1/chat/completions", `{"choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"stop"}]}`, "Authorization"},
		{ProviderGemini, "/v1beta/models/gemini-2.5-flash:generateContent", `{"candidates":[{"content":{"parts":[{"text":"{\"ok\":true}"}]},"finishReason":"STOP"}]}`, "x-goog-api-key"},
		{ProviderAnthropic, "/v1/messages", `{"content":[{"type":"text","text":"{\"ok\":true}"}],"stop_reason":"end_turn"}`, "x-api-key"},
	}
	for _, test := range cases {
		t.Run(test.id, func(t *testing.T) {
			client := &http.Client{Transport: providerRoundTrip(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path != test.endpoint || request.Header.Get(test.header) == "" {
					t.Fatalf("request %s header %s", request.URL, test.header)
				}
				payload, err := io.ReadAll(request.Body)
				if err != nil || !strings.Contains(string(payload), "object") {
					t.Fatalf("request omitted schema: %s, %v", payload, err)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(test.body)), Header: make(http.Header), Request: request}, nil
			})}
			modelName := ""
			if test.id == ProviderAnthropic {
				modelName = "test-model"
			}
			model, err := NewModelClient(ModelClientConfig{ModelConfig: ModelConfig{ProviderID: test.id, Model: modelName, APIKey: "test-key-123456"}, Client: client})
			if err != nil {
				t.Fatal(err)
			}
			result, err := model.Structured(context.Background(), StructuredRequest{Instructions: "return JSON", Input: "{}", SchemaName: "check", Schema: []byte(`{"type":"object"}`), MaxOutputTokens: 64})
			if err != nil || string(result) != `{"ok":true}` {
				t.Fatalf("result %s %v", result, err)
			}
		})
	}
}

func TestModelClientClassifiesTruncatedResponses(t *testing.T) {
	cases := []struct {
		id, body string
	}{
		{ProviderOpenRouter, `{"choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"length"}]}`},
		{ProviderGemini, `{"candidates":[{"content":{"parts":[{"text":"{\"ok\":true}"}]},"finishReason":"MAX_TOKENS"}]}`},
		{ProviderAnthropic, `{"content":[{"type":"text","text":"{\"ok\":true}"}],"stop_reason":"max_tokens"}`},
	}
	for _, test := range cases {
		t.Run(test.id, func(t *testing.T) {
			client := &http.Client{Transport: providerRoundTrip(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(test.body)), Header: make(http.Header), Request: request}, nil
			})}
			model, err := NewModelClient(ModelClientConfig{ModelConfig: ModelConfig{ProviderID: test.id, Model: "test-model", APIKey: "test-key"}, Client: client})
			if err != nil {
				t.Fatal(err)
			}
			_, err = model.Structured(context.Background(), StructuredRequest{Instructions: "return JSON", Input: "{}", SchemaName: "check", Schema: []byte(`{"type":"object"}`), MaxOutputTokens: 64})
			if !errors.Is(err, ErrIncomplete) {
				t.Fatalf("truncated response = %v", err)
			}
		})
	}
}

func TestModelClientKeepsPDFAndAudioOnOpenAI(t *testing.T) {
	client, err := NewModelClient(ModelClientConfig{ModelConfig: ModelConfig{ProviderID: ProviderGemini, APIKey: "test-key-123456"}})
	if err != nil {
		t.Fatal(err)
	}
	request := StructuredRequest{Instructions: "x", Input: "x", SchemaName: "x", Schema: []byte(`{"type":"object"}`), MaxOutputTokens: 1}
	if _, err := client.StructuredWithPDF(context.Background(), request, []byte("%PDF-x")); !errors.Is(err, ErrUnsupportedOperation) {
		t.Fatalf("PDF = %v", err)
	}
	if _, err := client.Transcribe(context.Background(), "wav", []byte("audio")); !errors.Is(err, ErrUnsupportedOperation) {
		t.Fatalf("audio = %v", err)
	}
}
