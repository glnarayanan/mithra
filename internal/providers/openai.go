package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
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
	ErrInvalidCredential   = errors.New("OpenAI credential is invalid")
	ErrRateLimited         = errors.New("OpenAI request is rate limited")
	ErrProviderUnavailable = errors.New("OpenAI is unavailable")
	ErrRefusal             = errors.New("OpenAI refused the request")
	ErrIncomplete          = errors.New("OpenAI response is incomplete")
	ErrInvalidResponse     = errors.New("OpenAI returned an invalid response")
)

type OpenAIConfig struct {
	APIKey  string
	Client  *http.Client
	Timeout time.Duration
}

type OpenAI struct {
	apiKey string
	client *http.Client
}

type StructuredRequest struct {
	Instructions    string
	Input           string
	SchemaName      string
	Schema          json.RawMessage
	MaxOutputTokens int
}

func NewOpenAI(config OpenAIConfig) (*OpenAI, error) {
	if strings.TrimSpace(config.APIKey) == "" || len(config.APIKey) > 1024 {
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
	}
	client.Timeout = timeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("redirect refused")
	}
	return &OpenAI{apiKey: config.APIKey, client: client}, nil
}

func (o *OpenAI) Validate(ctx context.Context) error {
	_, err := o.Structured(ctx, StructuredRequest{
		Instructions:    "Validate this API credential. Return the requested boolean only.",
		Input:           "Return true.",
		SchemaName:      "mithra_credential_check",
		Schema:          json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`),
		MaxOutputTokens: 32,
	})
	return err
}

func (o *OpenAI) Structured(ctx context.Context, input StructuredRequest) (json.RawMessage, error) {
	if o == nil || o.client == nil || !validStructuredRequest(input) {
		return nil, ErrInvalidResponse
	}
	var schema any
	if err := json.Unmarshal(input.Schema, &schema); err != nil {
		return nil, ErrInvalidResponse
	}
	payload, err := json.Marshal(map[string]any{
		"model":             ResponsesModel,
		"instructions":      input.Instructions,
		"input":             input.Input,
		"store":             false,
		"max_output_tokens": input.MaxOutputTokens,
		"text": map[string]any{"format": map[string]any{
			"type": "json_schema", "name": input.SchemaName, "strict": true, "schema": schema,
		}},
	})
	if err != nil {
		return nil, ErrInvalidResponse
	}
	return o.structuredPost(ctx, payload)
}

// StructuredWithPDF sends one explicitly confirmed PDF inline. Ordinary
// imports use Structured with locally extracted text and never call this path.
func (o *OpenAI) StructuredWithPDF(ctx context.Context, input StructuredRequest, pdf []byte) (json.RawMessage, error) {
	if o == nil || o.client == nil || !validStructuredRequest(input) || len(pdf) < 5 || len(pdf) > maxAudioBytes || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		return nil, ErrInvalidResponse
	}
	var schema any
	if err := json.Unmarshal(input.Schema, &schema); err != nil {
		return nil, ErrInvalidResponse
	}
	payload, err := json.Marshal(map[string]any{
		"model": inputModel(), "instructions": input.Instructions, "store": false, "max_output_tokens": input.MaxOutputTokens,
		"input": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": input.Input},
			map[string]any{"type": "input_file", "filename": "document.pdf", "file_data": "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(pdf)},
		}}},
		"text": map[string]any{"format": map[string]any{"type": "json_schema", "name": input.SchemaName, "strict": true, "schema": schema}},
	})
	if err != nil {
		return nil, ErrInvalidResponse
	}
	return o.structuredPost(ctx, payload)
}

func inputModel() string { return ResponsesModel }

func (o *OpenAI) structuredPost(ctx context.Context, payload []byte) (json.RawMessage, error) {
	response, err := o.post(ctx, responsesEndpoint, "application/json", bytes.NewReader(payload))
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
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, ErrInvalidResponse
	}
	if decoded.Status != "completed" {
		return nil, ErrIncomplete
	}
	var output string
	for _, item := range decoded.Output {
		if item.Type != "message" {
			continue
		}
		for _, content := range item.Content {
			switch content.Type {
			case "refusal":
				return nil, ErrRefusal
			case "output_text":
				if output != "" || content.Text == "" || len(content.Text) > maxProviderText {
					return nil, ErrInvalidResponse
				}
				output = content.Text
			}
		}
	}
	if output == "" || !json.Valid([]byte(output)) {
		return nil, ErrInvalidResponse
	}
	return json.RawMessage(output), nil
}

func (o *OpenAI) Transcribe(ctx context.Context, format string, audio []byte) (string, error) {
	if o == nil || o.client == nil || len(audio) == 0 || len(audio) > maxAudioBytes || !validAudioFormat(format) {
		return "", ErrInvalidResponse
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "audio."+format)
	if err != nil {
		return "", ErrInvalidResponse
	}
	if _, err := part.Write(audio); err != nil {
		return "", ErrInvalidResponse
	}
	if err := writer.WriteField("model", TranscriptionModel); err != nil {
		return "", ErrInvalidResponse
	}
	if err := writer.WriteField("response_format", "json"); err != nil {
		return "", ErrInvalidResponse
	}
	if err := writer.Close(); err != nil {
		return "", ErrInvalidResponse
	}
	response, err := o.post(ctx, transcriptionEndpoint, writer.FormDataContentType(), &body)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	content, err := boundedBody(response.Body)
	if err != nil {
		return "", ErrInvalidResponse
	}
	if err := providerStatus(response.StatusCode); err != nil {
		return "", err
	}
	var decoded struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &decoded); err != nil || strings.TrimSpace(decoded.Text) == "" || len(decoded.Text) > maxProviderText {
		return "", ErrInvalidResponse
	}
	return decoded.Text, nil
}

func (o *OpenAI) post(ctx context.Context, endpoint, contentType string, body io.Reader) (*http.Response, error) {
	parsed, _ := url.Parse(endpoint)
	if parsed == nil || parsed.Scheme != "https" || parsed.Host != "api.openai.com" {
		return nil, ErrProviderUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, ErrProviderUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+o.apiKey)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", "application/json")
	response, err := o.client.Do(request)
	if err != nil {
		return nil, ErrProviderUnavailable
	}
	if response.Request == nil || response.Request.URL == nil || response.Request.URL.Scheme != "https" || response.Request.URL.Host != "api.openai.com" {
		response.Body.Close()
		return nil, ErrProviderUnavailable
	}
	return response, nil
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
