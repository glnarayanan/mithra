package providers

import (
	"context"
	"encoding/json"
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

func TestAnthropicAcceptsThinkingAndFencedReplies(t *testing.T) {
	cases := map[string]string{
		"thinking first": `{"content":[{"type":"thinking","thinking":"","signature":"sig"},{"type":"text","text":"{\"ok\":true}"}],"stop_reason":"end_turn"}`,
		"redacted":       `{"content":[{"type":"redacted_thinking","data":"x"},{"type":"text","text":"{\"ok\":true}"}],"stop_reason":"end_turn"}`,
		"json fence":     `{"content":[{"type":"text","text":"` + "```json\\n{\\\"ok\\\":true}\\n```" + `"}],"stop_reason":"end_turn"}`,
		"bare fence":     `{"content":[{"type":"text","text":"` + "```\\n{\\\"ok\\\":true}\\n```" + `"}],"stop_reason":"end_turn"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			var maxTokens float64
			client := &http.Client{Transport: providerRoundTrip(func(request *http.Request) (*http.Response, error) {
				var payload map[string]any
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				maxTokens, _ = payload["max_tokens"].(float64)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: request}, nil
			})}
			model, err := NewModelClient(ModelClientConfig{ModelConfig: ModelConfig{ProviderID: ProviderAnthropic, Model: "claude-opus-5-5", APIKey: "test-key"}, Client: client})
			if err != nil {
				t.Fatal(err)
			}
			if err := model.Validate(context.Background()); err != nil {
				t.Fatalf("validate = %v", err)
			}
			if maxTokens < 32+reasoningHeadroom {
				t.Fatalf("max_tokens %v leaves no room for thinking", maxTokens)
			}
		})
	}
}

// Every provider style must give a reasoning model room to answer the small
// connection check, and must read the answer past its reasoning.
func TestConnectionCheckSurvivesReasoningModels(t *testing.T) {
	cases := []struct {
		name, id, budgetKey, body string
		check                     func(*testing.T, map[string]any)
	}{
		{"openai reasoning item", ProviderOpenAI, "max_output_tokens", `{"status":"completed","output":[{"type":"reasoning","summary":[]},{"type":"message","content":[{"type":"output_text","text":"{\"ok\":true}"}]}]}`, nil},
		{"chat think tags", ProviderDeepSeek, "max_tokens", `{"choices":[{"message":{"content":"<think>\nfine\n</think>\n{\"ok\":true}"},"finish_reason":"stop"}]}`, nil},
		{"chat fenced", ProviderXAI, "max_tokens", `{"choices":[{"message":{"content":"` + "```json\\n{\\\"ok\\\":true}\\n```" + `"},"finish_reason":"stop"}]}`, nil},
		{"chat content parts", ProviderMistral, "max_tokens", `{"choices":[{"message":{"content":[{"type":"thinking","thinking":[]},{"type":"text","text":"{\"ok\":true}"}]},"finish_reason":"stop"}]}`, nil},
		{"gemini split parts", ProviderGemini, "", `{"candidates":[{"content":{"parts":[{"text":"plan","thought":true},{"text":"{\"ok\":"},{"text":"true}"}]},"finishReason":"STOP"}]}`, func(t *testing.T, payload map[string]any) {
			config, _ := payload["generationConfig"].(map[string]any)
			if config["responseJsonSchema"] == nil || config["responseSchema"] != nil {
				t.Fatalf("gemini schema field = %v", config)
			}
			if budget, _ := config["maxOutputTokens"].(float64); budget < 32+reasoningHeadroom {
				t.Fatalf("gemini budget = %v", budget)
			}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: providerRoundTrip(func(request *http.Request) (*http.Response, error) {
				var payload map[string]any
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if test.budgetKey != "" {
					if budget, _ := payload[test.budgetKey].(float64); budget < 32+reasoningHeadroom {
						t.Fatalf("%s = %v", test.budgetKey, budget)
					}
				}
				if test.check != nil {
					test.check(t, payload)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(test.body)), Header: make(http.Header), Request: request}, nil
			})}
			model, err := NewModelClient(ModelClientConfig{ModelConfig: ModelConfig{ProviderID: test.id, APIKey: "test-key"}, Client: client})
			if err != nil {
				t.Fatal(err)
			}
			if err := model.Validate(context.Background()); err != nil {
				t.Fatalf("validate = %v", err)
			}
		})
	}
}

func TestProvidersReportRefusals(t *testing.T) {
	cases := []struct{ id, body string }{
		{ProviderOpenRouter, `{"choices":[{"message":{"content":null,"refusal":"no"},"finish_reason":"stop"}]}`},
		{ProviderGemini, `{"promptFeedback":{"blockReason":"SAFETY"},"candidates":[]}`},
		{ProviderGemini, `{"candidates":[{"content":{"parts":[]},"finishReason":"PROHIBITED_CONTENT"}]}`},
		{ProviderAnthropic, `{"content":[],"stop_reason":"refusal"}`},
	}
	for _, test := range cases {
		client := &http.Client{Transport: providerRoundTrip(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(test.body)), Header: make(http.Header), Request: request}, nil
		})}
		model, err := NewModelClient(ModelClientConfig{ModelConfig: ModelConfig{ProviderID: test.id, Model: "test-model", APIKey: "test-key"}, Client: client})
		if err != nil {
			t.Fatal(err)
		}
		if err := model.Validate(context.Background()); !errors.Is(err, ErrRefusal) {
			t.Fatalf("%s refusal = %v", test.id, err)
		}
	}
}

func TestOutputBudgetStaysWithinCallerLimits(t *testing.T) {
	for requested, want := range map[int]int{32: 4128, 4_000: 8096, 16_000: 16_384, 16_384: 16_384} {
		if got := outputBudget(requested); got != want {
			t.Fatalf("outputBudget(%d) = %d, want %d", requested, got, want)
		}
	}
}

func TestAnthropicRejectsUnexpectedBlocks(t *testing.T) {
	body := `{"content":[{"type":"tool_use","id":"x","name":"y","input":{}}],"stop_reason":"tool_use"}`
	client := &http.Client{Transport: providerRoundTrip(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: request}, nil
	})}
	model, err := NewModelClient(ModelClientConfig{ModelConfig: ModelConfig{ProviderID: ProviderAnthropic, Model: "claude-opus-5-5", APIKey: "test-key"}, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	if err := model.Validate(context.Background()); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("validate = %v", err)
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
