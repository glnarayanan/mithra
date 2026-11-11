package app

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/glnarayanan/mithra/internal/planning"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/providers"
	"github.com/glnarayanan/mithra/internal/secrets"
)

func (a *App) invitePartner(w http.ResponseWriter, r *http.Request, scope policy.ActorScope, csrf string) {
	email := r.PostForm.Get("email")
	if strings.TrimSpace(email) == "" || len(email) > 254 {
		a.renderSettings(r.Context(), w, scope, csrf, "", "Enter an email approved for this household.")
		return
	}
	invitation, err := a.auth.CreateInvitation(r.Context(), scope, email, invitationLifetime)
	if err != nil {
		a.renderSettings(r.Context(), w, scope, csrf, "", inviteError(err))
		return
	}
	link := a.canonicalLink("/auth/invitation", "token", invitation.Token)
	if err := a.mailer.Send(r.Context(), providers.Message{To: email, Subject: "You have been invited to Mithra", Text: "Join this Mithra household within seven days:\n" + link}); err != nil {
		logRequestError(a.logger, r.Context(), "invitation_delivery_failed")
		a.renderSettings(r.Context(), w, scope, csrf, "", "The invitation could not be delivered. Try again later.")
		return
	}
	a.renderSettings(r.Context(), w, scope, csrf, "Invitation sent. Your partner can choose a password from the secure link.", "")
}

func (a *App) saveProviderSetting(w http.ResponseWriter, r *http.Request, scope policy.ActorScope, csrf string) {
	candidate := secrets.ProviderConfig{ProviderID: r.PostForm.Get("provider"), Model: r.PostForm.Get("model"), BaseURL: r.PostForm.Get("base_url"), APIKey: r.PostForm.Get("api_key")}
	if r.PostForm.Get("action") == "save_openai" {
		candidate.ProviderID = providers.ProviderOpenAI
	}
	var checkErr error
	err := a.providerSettings.ReplaceProvider(r.Context(), scope, candidate, func(ctx context.Context, candidate secrets.ProviderConfig) error {
		client, err := providers.NewModelClient(providers.ModelClientConfig{ModelConfig: providers.ModelConfig{ProviderID: candidate.ProviderID, Model: candidate.Model, BaseURL: candidate.BaseURL, APIKey: candidate.APIKey}, Client: a.openAIClient})
		if err != nil {
			return err
		}
		if err := client.Validate(ctx); err != nil {
			checkErr = err
			logRequestError(a.logger, r.Context(), "provider_check_"+providerCheckReason(err))
			return err
		}
		return nil
	})
	if err != nil {
		a.renderSettings(r.Context(), w, scope, csrf, "", providerCheckMessage(checkErr))
		return
	}
	a.renderSettings(r.Context(), w, scope, csrf, "Model provider connected. Mithra never displays the saved key.", "")
}

// providerCheckReason names why a connection check failed, for the log only.
func providerCheckReason(err error) string {
	switch {
	case errors.Is(err, providers.ErrInvalidCredential):
		return "credential"
	case errors.Is(err, providers.ErrRateLimited):
		return "rate_limited"
	case errors.Is(err, providers.ErrRefusal):
		return "refusal"
	case errors.Is(err, providers.ErrIncomplete):
		return "incomplete"
	case errors.Is(err, providers.ErrInvalidResponse):
		return "invalid_response"
	default:
		return "unavailable"
	}
}

// providerCheckMessage says what the provider did, so the owner knows
// whether to change the key, the model, or simply wait.
func providerCheckMessage(err error) string {
	const unchanged = " Your existing connection is unchanged."
	switch providerCheckReason(err) {
	case "credential":
		return "The provider did not accept that API key." + unchanged + " Check the key and that it belongs to this provider."
	case "rate_limited":
		return "The provider is limiting requests for this key right now." + unchanged + " Try again in a few minutes."
	case "refusal":
		return "The provider declined the check request." + unchanged + " Try another model from this provider."
	case "incomplete", "invalid_response":
		return "The provider answered, but not with the JSON Mithra needs." + unchanged + " Try another model from this provider."
	}
	return "Mithra could not verify that connection." + unchanged + " Check the model name and other details, or try again later."
}

func (a *App) removeProviderSetting(w http.ResponseWriter, r *http.Request, scope policy.ActorScope, csrf string) {
	if err := a.providerSettings.RemoveProvider(r.Context(), scope); err != nil {
		a.renderSettings(r.Context(), w, scope, csrf, "", "Only the household owner can disconnect the model provider.")
		return
	}
	a.renderSettings(r.Context(), w, scope, csrf, "Model provider disconnected. Your saved household records remain available.", "")
}

func (a *App) saveHouseholdCurrency(w http.ResponseWriter, r *http.Request, scope policy.ActorScope, csrf string) {
	currency := strings.TrimSpace(r.PostForm.Get("currency"))
	if err := a.planningRecords.SetCurrency(r.Context(), scope, currency); err != nil {
		message := "Only the household owner can choose the currency sign."
		if errors.Is(err, planning.ErrInvalidRecord) {
			message = "Choose one of the listed currencies."
		}
		a.renderSettings(r.Context(), w, scope, csrf, "", message)
		return
	}
	status := "Money now shows " + currencyLabels[currency] + "."
	if currency == "" {
		status = "Money now shows amounts without a currency sign."
	}
	a.renderSettings(r.Context(), w, scope, csrf, status, "")
}

func (a *App) saveHouseholdLocale(w http.ResponseWriter, r *http.Request, scope policy.ActorScope, csrf string) {
	locale := strings.TrimSpace(r.PostForm.Get("locale"))
	if err := a.planningRecords.SetLocale(r.Context(), scope, locale); err != nil {
		message := "Only the household owner can choose the language and number format."
		if errors.Is(err, planning.ErrInvalidRecord) {
			message = "Choose one of the listed formats."
		}
		a.renderSettings(r.Context(), w, scope, csrf, "", message)
		return
	}
	a.renderSettings(r.Context(), w, scope, csrf, "Language and number format saved as "+localeLabels[locale][0]+".", "")
}

func (a *App) saveHouseholdTimezone(w http.ResponseWriter, r *http.Request, scope policy.ActorScope, csrf string) {
	zone := strings.TrimSpace(r.PostForm.Get("timezone"))
	if zone == "" || len(zone) > 64 {
		a.renderSettings(r.Context(), w, scope, csrf, "", "Enter a valid timezone such as Asia/Kolkata.")
		return
	}
	if err := a.planningRecords.SetTimezone(r.Context(), scope, zone); err != nil {
		a.renderSettings(r.Context(), w, scope, csrf, "", "Only the household owner can confirm a valid timezone.")
		return
	}
	a.renderSettings(r.Context(), w, scope, csrf, "Household timezone saved as "+zone+".", "")
}
