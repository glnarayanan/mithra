// Package providers contains deliberately small outbound HTTPS clients.
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const resendEndpoint = "https://api.resend.com/emails"

var ErrDelivery = errors.New("email delivery is unavailable")

// Mailer is the narrow seam used by authentication flows and deterministic
// tests. Message bodies are plain text only.
type Mailer interface {
	Send(context.Context, Message) error
}

type Message struct {
	To, Subject, Text string
}

type Resend struct {
	apiKey string
	from   string
	client *http.Client
}

type ResendConfig struct {
	APIKey string
	From   string
	Client *http.Client
}

func NewResend(cfg ResendConfig) (*Resend, error) {
	if strings.TrimSpace(cfg.APIKey) == "" || strings.TrimSpace(cfg.From) == "" {
		return nil, ErrDelivery
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Resend{apiKey: cfg.APIKey, from: cfg.From, client: cfg.Client}, nil
}

func (r *Resend) Send(ctx context.Context, message Message) error {
	if r == nil || strings.TrimSpace(message.To) == "" || strings.TrimSpace(message.Subject) == "" || strings.TrimSpace(message.Text) == "" {
		return ErrDelivery
	}
	payload, err := json.Marshal(struct {
		From    string `json:"from"`
		To      string `json:"to"`
		Subject string `json:"subject"`
		Text    string `json:"text"`
	}{r.from, message.To, message.Subject, message.Text})
	if err != nil {
		return ErrDelivery
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, resendEndpoint, bytes.NewReader(payload))
	if err != nil {
		return ErrDelivery
	}
	request.Header.Set("Authorization", "Bearer "+r.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return ErrDelivery
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return ErrDelivery
	}
	return nil
}
