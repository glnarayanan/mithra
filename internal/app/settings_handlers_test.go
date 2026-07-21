package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/providers"
	"github.com/glnarayanan/mithra/internal/secrets"
)

type appRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn appRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestProviderSwitchNeverReusesAnotherProviderKey(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	owner := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	scope := ownerScope(t, application, owner)
	if err := application.providerSettings.ReplaceProvider(context.Background(), scope, secrets.ProviderConfig{ProviderID: providers.ProviderOpenAI, APIKey: "openai-secret"}, func(context.Context, secrets.ProviderConfig) error { return nil }); err != nil {
		t.Fatal(err)
	}
	validated := false
	err := application.providerSettings.ReplaceProvider(context.Background(), scope, secrets.ProviderConfig{ProviderID: providers.ProviderAnthropic, Model: "test-model", BaseURL: "https://api.anthropic.com"}, func(context.Context, secrets.ProviderConfig) error {
		validated = true
		return nil
	})
	if !errors.Is(err, secrets.ErrSettingsCredential) || validated {
		t.Fatalf("required-key switch = %v, validated=%t", err, validated)
	}
	err = application.providerSettings.ReplaceProvider(context.Background(), scope, secrets.ProviderConfig{ProviderID: providers.ProviderOllama, Model: "local-model", BaseURL: "http://127.0.0.1:11434/v1"}, func(_ context.Context, config secrets.ProviderConfig) error {
		if config.APIKey != "" {
			t.Fatalf("prior key sent to local provider: %q", config.APIKey)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := application.providerSettings.ProviderConfig(context.Background(), scope)
	if err != nil || config.ProviderID != providers.ProviderOllama || config.APIKey != "" {
		t.Fatalf("stored local config = %#v, %v", config, err)
	}
}

func TestOwnerOpenAISettingsValidateEncryptPreserveAndRemove(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com", "partner@example.com")
	owner := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	application.openAIClient = openAIValidationClient(http.StatusOK)

	settings := authenticatedSettingsRequest(owner, http.MethodGet, nil)
	response := serve(application, settings)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "<h1 id=\"page-title\">Settings</h1>") || !strings.Contains(response.Body.String(), "Household access") || !strings.Contains(response.Body.String(), "Not connected") || !strings.Contains(response.Body.String(), "AI features stay off") || !strings.Contains(response.Body.String(), `data-provider-select`) || !strings.Contains(response.Body.String(), `data-default-model="gpt-5.4-mini"`) {
		t.Fatalf("unconfigured settings = %d %q", response.Code, response.Body.String())
	}
	var queued int
	if err := application.db.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("unconfigured settings queued work: %d, %v", queued, err)
	}

	firstKey := "sk-mithra-valid-first-key"
	save := authenticatedSettingsRequest(owner, http.MethodPost, url.Values{"action": {"save_provider"}, "provider": {"openai"}, "model": {"gpt-5.4-mini"}, "base_url": {"https://api.openai.com/v1"}, "api_key": {firstKey}})
	saved := serve(application, save)
	if saved.Code != http.StatusOK || !strings.Contains(saved.Body.String(), "Model provider connected") || !strings.Contains(saved.Body.String(), "Connected to OpenAI · gpt-5.4-mini") || strings.Contains(saved.Body.String(), firstKey) {
		t.Fatalf("saved settings = %d %q", saved.Code, saved.Body.String())
	}
	var ciphertext []byte
	if err := application.db.QueryRow(`SELECT encrypted_api_key FROM household_openai_settings`).Scan(&ciphertext); err != nil || bytes.Contains(ciphertext, []byte(firstKey)) {
		t.Fatalf("stored credential = %q, %v", ciphertext, err)
	}
	key, err := application.providerSettings.OpenAIKey(context.Background(), ownerScope(t, application, owner))
	if err != nil || key != firstKey {
		t.Fatalf("server credential = %q, %v", key, err)
	}

	kept := serve(application, authenticatedSettingsRequest(owner, http.MethodPost, url.Values{"action": {"save_provider"}, "provider": {"openai"}, "model": {"gpt-5.4-mini"}, "base_url": {"https://api.openai.com/v1"}}))
	if kept.Code != http.StatusOK || !strings.Contains(kept.Body.String(), "Model provider connected") {
		t.Fatalf("blank key update = %d %q", kept.Code, kept.Body.String())
	}
	key, err = application.providerSettings.OpenAIKey(context.Background(), ownerScope(t, application, owner))
	if err != nil || key != firstKey {
		t.Fatalf("blank key did not preserve the key = %q, %v", key, err)
	}

	application.openAIClient = openAIValidationClient(http.StatusUnauthorized)
	replacement := "sk-mithra-invalid-replacement"
	failed := serve(application, authenticatedSettingsRequest(owner, http.MethodPost, url.Values{"action": {"save_provider"}, "provider": {"openai"}, "model": {"gpt-5.4-mini"}, "base_url": {"https://api.openai.com/v1"}, "api_key": {replacement}}))
	if failed.Code != http.StatusOK || !strings.Contains(failed.Body.String(), "existing connection is unchanged") || strings.Contains(failed.Body.String(), replacement) {
		t.Fatalf("failed replacement = %d %q", failed.Code, failed.Body.String())
	}
	key, err = application.providerSettings.OpenAIKey(context.Background(), ownerScope(t, application, owner))
	if err != nil || key != firstKey {
		t.Fatalf("working key was replaced = %q, %v", key, err)
	}

	application.openAIClient = &http.Client{Transport: appRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("provider unavailable")
	})}
	unavailable := serve(application, authenticatedSettingsRequest(owner, http.MethodPost, url.Values{"action": {"save_provider"}, "provider": {"openai"}, "model": {"gpt-5.4-mini"}, "base_url": {"https://api.openai.com/v1"}, "api_key": {replacement}}))
	if unavailable.Code != http.StatusOK || !strings.Contains(unavailable.Body.String(), "could not verify that connection") || !strings.Contains(unavailable.Body.String(), "existing connection is unchanged") {
		t.Fatalf("unavailable provider = %d %q", unavailable.Code, unavailable.Body.String())
	}

	invite := serve(application, settingsPost(owner, "partner@example.com"))
	if invite.Code != http.StatusOK {
		t.Fatalf("invite partner = %d", invite.Code)
	}
	partnerToken := tokenFromMessage(t, mailer.last(t), "token")
	partner := activateInvitation(t, application, "a partner secure password", bootstrapInvitation(t, application, partnerToken))
	denied := serve(application, authenticatedSettingsRequest(partner, http.MethodPost, url.Values{"action": {"remove_openai"}}))
	if denied.Code != http.StatusOK || !strings.Contains(denied.Body.String(), "Only the household owner can disconnect the model provider") {
		t.Fatalf("partner removal = %d %q", denied.Code, denied.Body.String())
	}

	removed := serve(application, authenticatedSettingsRequest(owner, http.MethodPost, url.Values{"action": {"remove_openai"}}))
	if removed.Code != http.StatusOK || !strings.Contains(removed.Body.String(), "Model provider disconnected") || !strings.Contains(removed.Body.String(), "Not connected") {
		t.Fatalf("removed settings = %d %q", removed.Code, removed.Body.String())
	}
	if configured, err := application.providerSettings.Configured(context.Background(), ownerScope(t, application, owner)); err != nil || configured {
		t.Fatalf("configured after remove = %t, %v", configured, err)
	}
}

func authenticatedSettingsRequest(session browserSession, method string, values url.Values) *http.Request {
	if values == nil {
		request := httptest.NewRequest(method, "/settings", nil)
		request.AddCookie(session.session)
		request.AddCookie(session.csrf)
		return request
	}
	values.Set("csrf", session.csrf.Value)
	return authForm(method, "/settings", values, []*http.Cookie{session.session, session.csrf})
}

func ownerScope(t *testing.T, application *App, session browserSession) policy.ActorScope {
	t.Helper()
	scope, err := application.auth.Authenticate(context.Background(), session.session.Value)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func openAIValidationClient(status int) *http.Client {
	return &http.Client{Transport: appRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"ok\":true}"}]}]}`
		if status != http.StatusOK {
			body = `{"error":{"message":"upstream details"}}`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
}
