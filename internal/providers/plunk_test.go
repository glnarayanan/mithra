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

func TestPlunkUsesFixedHTTPSEndpointAndGenericErrors(t *testing.T) {
	t.Parallel()
	called := false
	client := &http.Client{Transport: roundTripper(func(request *http.Request) (*http.Response, error) {
		called = true
		if request.URL.String() != plunkEndpoint || request.Header.Get("Authorization") != "Bearer sk_secret" || request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unsafe Plunk request: %s %#v", request.URL, request.Header)
		}
		body, err := io.ReadAll(request.Body)
		var payload struct {
			From struct {
				Name  string `json:"name"`
				Email string `json:"email"`
			} `json:"from"`
			To      string `json:"to"`
			Subject string `json:"subject"`
			Body    string `json:"body"`
		}
		if err != nil || json.Unmarshal(body, &payload) != nil || payload.From.Name != "Mithra" || payload.From.Email != "mail@example.com" || payload.To != "person@example.com" || payload.Subject != "Mithra" || payload.Body != "Use &lt;this&gt;<br>https://mithra.example/reset?token=opaque&amp;next=1" {
			t.Fatalf("Plunk JSON = %q, %v", body, err)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(`{"success":true}`)), Header: make(http.Header)}, nil
	})}
	mailer, err := NewPlunk(PlunkConfig{APIKey: "sk_secret", From: "Mithra <mail@example.com>", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	if err := mailer.Send(context.Background(), Message{To: "person@example.com", Subject: "Mithra", Text: "Use <this>\nhttps://mithra.example/reset?token=opaque&next=1"}); err != nil || !called {
		t.Fatalf("send = %v called=%v", err, called)
	}
}

func TestPlunkRejectsPublicKeyAndNeverReturnsProviderBody(t *testing.T) {
	t.Parallel()
	if _, err := NewPlunk(PlunkConfig{APIKey: "pk_public", From: "mail@example.com"}); !errors.Is(err, ErrDelivery) {
		t.Fatalf("public key accepted: %v", err)
	}
	client := &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(bytes.NewBufferString("provider secret body")), Header: make(http.Header)}, nil
	})}
	mailer, err := NewPlunk(PlunkConfig{APIKey: "sk_secret", From: "mail@example.com", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	if err := mailer.Send(context.Background(), Message{To: "person@example.com", Subject: "Mithra", Text: "safe text"}); !errors.Is(err, ErrDelivery) || err.Error() != ErrDelivery.Error() {
		t.Fatalf("error leaked provider detail: %v", err)
	}
}

func TestPlunkRequiresSuccessEnvelope(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(`{"success":false}`)), Header: make(http.Header)}, nil
	})}
	mailer, err := NewPlunk(PlunkConfig{APIKey: "sk_secret", From: "mail@example.com", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	if err := mailer.Send(context.Background(), Message{To: "person@example.com", Subject: "Mithra", Text: "safe text"}); !errors.Is(err, ErrDelivery) {
		t.Fatalf("false success accepted: %v", err)
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
