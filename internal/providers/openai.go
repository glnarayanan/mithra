package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	responsesEndpoint     = "https://api.openai.com/v1/responses"
	transcriptionEndpoint = "https://api.openai.com/v1/audio/transcriptions"
	ResponsesModel        = "gpt-5.4-mini"
	TranscriptionModel    = "gpt-4o-mini-transcribe"
	maxProviderResponse   = 1 << 20
	maxProviderText       = 256 << 10
	maxAudioBytes         = 10 << 20
)

var (
	ErrInvalidCredential    = errors.New("provider credential is invalid")
	ErrRateLimited          = errors.New("provider request is rate limited")
	ErrProviderUnavailable  = errors.New("provider is unavailable")
	ErrRefusal              = errors.New("provider refused the request")
	ErrIncomplete           = errors.New("provider response is incomplete")
	ErrInvalidResponse      = errors.New("provider returned an invalid response")
	ErrUnsupportedOperation = errors.New("provider does not support this operation")
)

// OpenAIConfig remains for tests and callers that use OpenAI's native API.
type OpenAIConfig struct {
	APIKey  string
	Client  *http.Client
	Timeout time.Duration
}

type ModelClientConfig struct {
	ModelConfig
	Client  *http.Client
	Timeout time.Duration
}

type ModelClient struct {
	config   ModelConfig
	provider ModelProvider
	client   *http.Client
}

var customProviderTransport = publicProviderTransport()

// OpenAI is kept as an alias for the existing OpenAI-only test seam.
type OpenAI = ModelClient

type StructuredRequest struct {
	Instructions    string
	Input           string
	SchemaName      string
	Schema          json.RawMessage
	MaxOutputTokens int
}

func NewOpenAI(config OpenAIConfig) (*OpenAI, error) {
	return NewModelClient(ModelClientConfig{ModelConfig: ModelConfig{ProviderID: ProviderOpenAI, APIKey: config.APIKey}, Client: config.Client, Timeout: config.Timeout})
}

func NewModelClient(config ModelClientConfig) (*ModelClient, error) {
	normalized, provider, err := NormalizeModelConfig(config.ModelConfig)
	if err != nil {
		return nil, ErrInvalidCredential
	}
	timeout := config.Timeout
	if timeout == 0 {
		timeout = 45 * time.Second
	}
	if timeout < time.Millisecond || timeout > 2*time.Minute {
		return nil, ErrProviderUnavailable
	}
	client := &http.Client{}
	if config.Client != nil {
		*client = *config.Client
	} else if provider.ID == ProviderCustom {
		client.Transport = customProviderTransport
	}
	client.Timeout = timeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }
	return &ModelClient{config: normalized, provider: provider, client: client}, nil
}

func (c *ModelClient) Provider() ModelProvider {
	if c == nil {
		return ModelProvider{}
	}
	return c.provider
}
func (c *ModelClient) Model() string {
	if c == nil {
		return ""
	}
	return c.config.Model
}

func (c *ModelClient) Validate(ctx context.Context) error {
	_, err := c.Structured(ctx, StructuredRequest{Instructions: "Validate this API credential. Return the requested boolean only.", Input: "Return true.", SchemaName: "mithra_credential_check", Schema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`), MaxOutputTokens: 32})
	return err
}

func (c *ModelClient) Structured(ctx context.Context, input StructuredRequest) (json.RawMessage, error) {
	if c == nil || c.client == nil || !validStructuredRequest(input) {
		return nil, ErrInvalidResponse
	}
	var schema any
	if json.Unmarshal(input.Schema, &schema) != nil {
		return nil, ErrInvalidResponse
	}
	switch c.provider.Style {
	case StyleResponses:
		return c.responses(ctx, input, schema)
	case StyleOpenAI:
		return c.openAICompatible(ctx, input, schema)
	case StyleGemini:
		return c.gemini(ctx, input, schema)
	case StyleAnthropic:
		return c.anthropic(ctx, input, schema)
	default:
		return nil, ErrUnsupportedOperation
	}
}

// StructuredWithPDF sends an explicitly confirmed PDF only to OpenAI.
func (c *ModelClient) StructuredWithPDF(ctx context.Context, input StructuredRequest, pdf []byte) (json.RawMessage, error) {
	if c == nil || c.provider.ID != ProviderOpenAI {
		return nil, ErrUnsupportedOperation
	}
	if !validStructuredRequest(input) || len(pdf) < 5 || len(pdf) > maxAudioBytes || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		return nil, ErrInvalidResponse
	}
	var schema any
	if json.Unmarshal(input.Schema, &schema) != nil {
		return nil, ErrInvalidResponse
	}
	payload, err := json.Marshal(map[string]any{"model": c.config.Model, "instructions": input.Instructions, "store": false, "max_output_tokens": outputBudget(input.MaxOutputTokens),
		"input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": input.Input}, map[string]any{"type": "input_file", "filename": "document.pdf", "file_data": "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(pdf)}}}},
		"text":  map[string]any{"format": map[string]any{"type": "json_schema", "name": input.SchemaName, "strict": true, "schema": schema}}})
	if err != nil {
		return nil, ErrInvalidResponse
	}
	return c.responsesPost(ctx, payload)
}

func (c *ModelClient) Transcribe(ctx context.Context, format string, audio []byte) (string, error) {
	if c == nil || c.provider.ID != ProviderOpenAI {
		return "", ErrUnsupportedOperation
	}
	if len(audio) == 0 || len(audio) > maxAudioBytes || !validAudioFormat(format) {
		return "", ErrInvalidResponse
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "audio."+format)
	if err != nil {
		return "", ErrInvalidResponse
	}
	if _, err = part.Write(audio); err != nil {
		return "", ErrInvalidResponse
	}
	if writer.WriteField("model", TranscriptionModel) != nil || writer.WriteField("response_format", "json") != nil || writer.Close() != nil {
		return "", ErrInvalidResponse
	}
	response, err := c.post(ctx, c.endpoint("audio/transcriptions"), writer.FormDataContentType(), &body, "bearer")
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	bodyBytes, err := boundedBody(response.Body)
	if err != nil {
		return "", ErrInvalidResponse
	}
	if err := providerStatus(response.StatusCode); err != nil {
		return "", err
	}
	var decoded struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(bodyBytes, &decoded) != nil || strings.TrimSpace(decoded.Text) == "" || len(decoded.Text) > maxProviderText {
		return "", ErrInvalidResponse
	}
	return decoded.Text, nil
}

func (c *ModelClient) responses(ctx context.Context, input StructuredRequest, schema any) (json.RawMessage, error) {
	payload, err := json.Marshal(map[string]any{"model": c.config.Model, "instructions": input.Instructions, "input": input.Input, "store": false, "max_output_tokens": outputBudget(input.MaxOutputTokens), "text": map[string]any{"format": map[string]any{"type": "json_schema", "name": input.SchemaName, "strict": true, "schema": schema}}})
	if err != nil {
		return nil, ErrInvalidResponse
	}
	return c.responsesPost(ctx, payload)
}

func (c *ModelClient) responsesPost(ctx context.Context, payload []byte) (json.RawMessage, error) {
	response, err := c.post(ctx, c.endpoint("responses"), "application/json", bytes.NewReader(payload), "bearer")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := boundedBody(response.Body)
	if err != nil {
		return nil, ErrInvalidResponse
	}
	if err := providerStatus(response.StatusCode); err != nil {
		return nil, err
	}
	var decoded struct {
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Refusal string `json:"refusal"`
			} `json:"content"`
		} `json:"output"`
	}
	if json.Unmarshal(body, &decoded) != nil {
		return nil, ErrInvalidResponse
	}
	if decoded.Status != "completed" {
		return nil, ErrIncomplete
	}
	var output string
	for _, item := range decoded.Output {
		if item.Type == "message" {
			for _, content := range item.Content {
				if content.Type == "refusal" {
					return nil, ErrRefusal
				}
				if content.Type == "output_text" {
					if output != "" {
						return nil, ErrInvalidResponse
					}
					output = content.Text
				}
			}
		}
	}
	return boundedJSON(output)
}

func (c *ModelClient) openAICompatible(ctx context.Context, input StructuredRequest, schema any) (json.RawMessage, error) {
	payload, err := json.Marshal(map[string]any{"model": c.config.Model, "messages": []map[string]string{{"role": "system", "content": schemaInstructions(input.Instructions, schema)}, {"role": "user", "content": input.Input}}, "max_tokens": outputBudget(input.MaxOutputTokens)})
	if err != nil {
		return nil, ErrInvalidResponse
	}
	response, err := c.post(ctx, c.endpoint("chat/completions"), "application/json", bytes.NewReader(payload), "bearer")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := boundedBody(response.Body)
	if err != nil {
		return nil, ErrInvalidResponse
	}
	if err := providerStatus(response.StatusCode); err != nil {
		return nil, err
	}
	var decoded struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
				Refusal string          `json:"refusal"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &decoded) != nil || len(decoded.Choices) != 1 {
		return nil, ErrInvalidResponse
	}
	choice := decoded.Choices[0]
	if choice.FinishReason == "content_filter" || strings.TrimSpace(choice.Message.Refusal) != "" {
		return nil, ErrRefusal
	}
	if choice.FinishReason == "length" {
		return nil, ErrIncomplete
	}
	text, ok := chatContentText(choice.Message.Content)
	if !ok {
		return nil, ErrInvalidResponse
	}
	return boundedJSON(answerJSON(text))
}

// chatContentText reads a chat completion's content, which is a string on
// most providers and a list of typed parts on some (Mistral reasoning models
// return thinking parts before the text part).
func chatContentText(raw json.RawMessage) (string, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, true
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return "", false
	}
	for _, part := range parts {
		if part.Type == "text" {
			text += part.Text
		}
	}
	return text, true
}

func (c *ModelClient) gemini(ctx context.Context, input StructuredRequest, schema any) (json.RawMessage, error) {
	// responseJsonSchema takes standard JSON Schema. The older responseSchema
	// field takes an OpenAPI subset and rejects keywords such as
	// additionalProperties, which every Mithra schema uses.
	payload, err := json.Marshal(map[string]any{"systemInstruction": map[string]any{"parts": []map[string]string{{"text": input.Instructions}}}, "contents": []map[string]any{{"role": "user", "parts": []map[string]string{{"text": input.Input}}}}, "generationConfig": map[string]any{"responseMimeType": "application/json", "responseJsonSchema": schema, "maxOutputTokens": outputBudget(input.MaxOutputTokens)}})
	if err != nil {
		return nil, ErrInvalidResponse
	}
	endpoint := strings.TrimRight(c.config.BaseURL, "/") + "/v1beta/models/" + url.PathEscape(c.config.Model) + ":generateContent"
	response, err := c.post(ctx, endpoint, "application/json", bytes.NewReader(payload), "gemini")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := boundedBody(response.Body)
	if err != nil {
		return nil, ErrInvalidResponse
	}
	if err := providerStatus(response.StatusCode); err != nil {
		return nil, err
	}
	var decoded struct {
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
	}
	if json.Unmarshal(body, &decoded) != nil {
		return nil, ErrInvalidResponse
	}
	if decoded.PromptFeedback.BlockReason != "" {
		return nil, ErrRefusal
	}
	if len(decoded.Candidates) != 1 {
		return nil, ErrInvalidResponse
	}
	switch decoded.Candidates[0].FinishReason {
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY":
		return nil, ErrRefusal
	case "MAX_TOKENS":
		return nil, ErrIncomplete
	}
	// The answer can arrive in several parts; thought summaries are skipped.
	var output string
	for _, part := range decoded.Candidates[0].Content.Parts {
		if !part.Thought {
			output += part.Text
		}
	}
	return boundedJSON(answerJSON(output))
}

// reasoningHeadroom is added to every output-token limit. Most current models
// reason before they answer (Claude, GPT-5, Gemini 2.5+, Grok, DeepSeek and
// others), and that reasoning counts toward the same limit. Without headroom
// a small request such as the connection check is cut off before it answers.
const reasoningHeadroom = 4096

// outputBudget gives a request room to reason, never going below what the
// caller asked for or above the largest budget callers already use.
func outputBudget(requested int) int {
	return min(requested+reasoningHeadroom, max(requested, 16_384))
}

func (c *ModelClient) anthropic(ctx context.Context, input StructuredRequest, schema any) (json.RawMessage, error) {
	payload, err := json.Marshal(map[string]any{"model": c.config.Model, "max_tokens": outputBudget(input.MaxOutputTokens), "system": schemaInstructions(input.Instructions, schema), "messages": []map[string]string{{"role": "user", "content": input.Input}}})
	if err != nil {
		return nil, ErrInvalidResponse
	}
	response, err := c.post(ctx, c.endpoint("v1/messages"), "application/json", bytes.NewReader(payload), "anthropic")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := boundedBody(response.Body)
	if err != nil {
		return nil, ErrInvalidResponse
	}
	if err := providerStatus(response.StatusCode); err != nil {
		return nil, err
	}
	var decoded struct {
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(body, &decoded) != nil {
		return nil, ErrInvalidResponse
	}
	if decoded.StopReason == "refusal" {
		return nil, ErrRefusal
	}
	if decoded.StopReason == "max_tokens" {
		return nil, ErrIncomplete
	}
	// Thinking blocks come before the answer; only text blocks carry it.
	var output string
	for _, block := range decoded.Content {
		switch block.Type {
		case "text":
			output += block.Text
		case "thinking", "redacted_thinking":
		default:
			return nil, ErrInvalidResponse
		}
	}
	return boundedJSON(answerJSON(output))
}

// answerJSON trims what models without schema enforcement put around a JSON
// reply: a leading <think> section (DeepSeek R1, Qwen, MiniMax and others)
// and one Markdown code fence.
func answerJSON(text string) string {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "<think>") {
		end := strings.Index(trimmed, "</think>")
		if end < 0 {
			return text
		}
		trimmed = strings.TrimSpace(trimmed[end+len("</think>"):])
		text = trimmed
	}
	if !strings.HasPrefix(trimmed, "```") || !strings.HasSuffix(trimmed, "```") || len(trimmed) < 6 {
		return text
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(trimmed, "```"), "```")
	if newline := strings.IndexByte(inner, '\n'); newline >= 0 && strings.TrimSpace(inner[:newline]) != "" && !strings.ContainsAny(inner[:newline], "{[") {
		inner = inner[newline+1:]
	}
	return strings.TrimSpace(inner)
}

func (c *ModelClient) endpoint(path string) string {
	return strings.TrimRight(c.config.BaseURL, "/") + "/" + path
}

func (c *ModelClient) post(ctx context.Context, endpoint, contentType string, body io.Reader, auth string) (*http.Response, error) {
	parsed, err := url.Parse(endpoint)
	base, baseErr := url.Parse(c.config.BaseURL)
	if err != nil || baseErr != nil || !sameOrigin(parsed, base) {
		return nil, ErrProviderUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, ErrProviderUnavailable
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", "application/json")
	switch auth {
	case "bearer":
		if c.config.APIKey != "" {
			request.Header.Set("Authorization", "Bearer "+c.config.APIKey)
		}
	case "gemini":
		request.Header.Set("x-goog-api-key", c.config.APIKey)
	case "anthropic":
		request.Header.Set("x-api-key", c.config.APIKey)
		request.Header.Set("anthropic-version", "2023-06-01")
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, ErrProviderUnavailable
	}
	if response.Request == nil || response.Request.URL == nil || !sameOrigin(response.Request.URL, base) {
		response.Body.Close()
		return nil, ErrProviderUnavailable
	}
	return response, nil
}

func sameOrigin(left, right *url.URL) bool {
	return left != nil && right != nil && left.Scheme == right.Scheme && strings.EqualFold(left.Host, right.Host)
}
func schemaInstructions(instructions string, schema any) string {
	encoded, _ := json.Marshal(schema)
	return instructions + "\nReturn one JSON object that matches this JSON Schema exactly:\n" + string(encoded)
}
func publicProviderTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		dialer := net.Dialer{Timeout: 10 * time.Second}
		for _, address := range addresses {
			if isPublicProviderIP(address.IP) {
				return dialer.DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
			}
		}
		return nil, fmt.Errorf("provider address is not public")
	}
	return transport
}
func boundedJSON(text string) (json.RawMessage, error) {
	if len(text) == 0 || len(text) > maxProviderText || !json.Valid([]byte(text)) {
		return nil, ErrInvalidResponse
	}
	var value any
	if json.Unmarshal([]byte(text), &value) != nil {
		return nil, ErrInvalidResponse
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, ErrInvalidResponse
	}
	return json.RawMessage(text), nil
}
func boundedBody(body io.Reader) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(body, maxProviderResponse+1))
	if err != nil || len(content) > maxProviderResponse {
		return nil, ErrInvalidResponse
	}
	return content, nil
}
func providerStatus(status int) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ErrInvalidCredential
	case status == http.StatusTooManyRequests:
		return ErrRateLimited
	default:
		return ErrProviderUnavailable
	}
}
func validStructuredRequest(input StructuredRequest) bool {
	if len(input.Instructions) < 1 || len(input.Instructions) > 32<<10 || len(input.Input) < 1 || len(input.Input) > 512<<10 || len(input.Schema) < 2 || len(input.Schema) > 64<<10 || input.MaxOutputTokens < 1 || input.MaxOutputTokens > 16_384 || len(input.SchemaName) < 1 || len(input.SchemaName) > 64 {
		return false
	}
	for _, character := range input.SchemaName {
		if character != '_' && character != '-' && (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}
func validAudioFormat(format string) bool {
	switch format {
	case "flac", "mp3", "mp4", "mpeg", "mpga", "m4a", "ogg", "wav", "webm":
		return true
	default:
		return false
	}
}
