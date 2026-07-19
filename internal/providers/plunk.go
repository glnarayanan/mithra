// Package providers contains deliberately small outbound HTTPS clients.
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"net/mail"
	"strings"
	"time"
)

const plunkEndpoint = "https://next-api.useplunk.com/v1/send"

var ErrDelivery = errors.New("email delivery is unavailable")

// Mailer is the narrow seam used by authentication flows and deterministic
// tests. Message bodies are plain text only.
type Mailer interface {
	Send(context.Context, Message) error
}

type Message struct {
	To, Subject, Text string
}

type Plunk struct {
	apiKey string
	from   mail.Address
	client *http.Client
}

type PlunkConfig struct {
	APIKey string
	From   string
	Client *http.Client
}

func NewPlunk(cfg PlunkConfig) (*Plunk, error) {
	from, err := mail.ParseAddress(strings.TrimSpace(cfg.From))
	if !strings.HasPrefix(strings.TrimSpace(cfg.APIKey), "sk_") || err != nil {
		return nil, ErrDelivery
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 10 * time.Second}
	}
	client := *cfg.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Plunk{apiKey: strings.TrimSpace(cfg.APIKey), from: *from, client: &client}, nil
}

func (p *Plunk) Send(ctx context.Context, message Message) error {
	if p == nil || strings.TrimSpace(message.To) == "" || strings.TrimSpace(message.Subject) == "" || strings.TrimSpace(message.Text) == "" {
		return ErrDelivery
	}
	body := strings.ReplaceAll(html.EscapeString(message.Text), "\n", "<br>")
	payload, err := json.Marshal(struct {
		From struct {
			Name  string `json:"name,omitempty"`
			Email string `json:"email"`
		} `json:"from"`
		To      string `json:"to"`
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}{struct {
		Name  string `json:"name,omitempty"`
		Email string `json:"email"`
	}{p.from.Name, p.from.Address}, message.To, message.Subject, body})
	if err != nil {
		return ErrDelivery
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, plunkEndpoint, bytes.NewReader(payload))
	if err != nil {
		return ErrDelivery
	}
	request.Header.Set("Authorization", "Bearer "+p.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		return ErrDelivery
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	var result struct {
		Success bool `json:"success"`
	}
	if err != nil || response.StatusCode != http.StatusOK || json.Unmarshal(responseBody, &result) != nil || !result.Success {
		return ErrDelivery
	}
	return nil
}
