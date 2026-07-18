package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
)

func TestResendUsesFixedHTTPSEndpointAndGenericErrors(t *testing.T) {
	t.Parallel()
	called := false
	client := &http.Client{Transport: roundTripper(func(request *http.Request) (*http.Response, error) {
		called = true
		if request.URL.String() != resendEndpoint || request.Header.Get("Authorization") != "Bearer secret" || request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unsafe resend request: %s %#v", request.URL, request.Header)
		}
		body, err := io.ReadAll(request.Body)
		var payload struct {
			From    string `json:"from"`
			To      string `json:"to"`
			Subject string `json:"subject"`
			Text    string `json:"text"`
		}
		if err != nil || json.Unmarshal(body, &payload) != nil || payload.From != "Mithra <mail@example.com>" || payload.To != "person@example.com" || payload.Subject != "Mithra" || payload.Text != "https://mithra.example/reset?token=opaque" {
			t.Fatalf("resend JSON = %q, %v", body, err)
		}
		return &http.Response{StatusCode: http.StatusAccepted, Body: http.NoBody, Header: make(http.Header)}, nil
	})}
	mailer, err := NewResend(ResendConfig{APIKey: "secret", From: "Mithra <mail@example.com>", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	if err := mailer.Send(context.Background(), Message{To: "person@example.com", Subject: "Mithra", Text: "https://mithra.example/reset?token=opaque"}); err != nil || !called {
		t.Fatalf("send = %v called=%v", err, called)
	}
}

func TestResendNeverReturnsProviderBody(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(bytes.NewBufferString("provider secret body")), Header: make(http.Header)}, nil
	})}
	mailer, err := NewResend(ResendConfig{APIKey: "secret", From: "mail@example.com", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	if err := mailer.Send(context.Background(), Message{To: "person@example.com", Subject: "Mithra", Text: "safe text"}); !errors.Is(err, ErrDelivery) || err.Error() != ErrDelivery.Error() {
		t.Fatalf("error leaked provider detail: %v", err)
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
