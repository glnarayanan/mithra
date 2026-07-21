package app

import (
	"context"
	"net/http"
	"strings"

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
	err := a.providerSettings.ReplaceProvider(r.Context(), scope, candidate, func(ctx context.Context, candidate secrets.ProviderConfig) error {
		client, err := providers.NewModelClient(providers.ModelClientConfig{ModelConfig: providers.ModelConfig{ProviderID: candidate.ProviderID, Model: candidate.Model, BaseURL: candidate.BaseURL, APIKey: candidate.APIKey}, Client: a.openAIClient})
		if err != nil {
			return err
		}
		return client.Validate(ctx)
	})
	if err != nil {
		a.renderSettings(r.Context(), w, scope, csrf, "", "Mithra could not verify that connection. Your existing connection is unchanged. Check the details or try again later.")
		return
	}
	a.renderSettings(r.Context(), w, scope, csrf, "Model provider connected. Mithra never displays the saved key.", "")
}

func (a *App) removeProviderSetting(w http.ResponseWriter, r *http.Request, scope policy.ActorScope, csrf string) {
	if err := a.providerSettings.RemoveProvider(r.Context(), scope); err != nil {
		a.renderSettings(r.Context(), w, scope, csrf, "", "Only the household owner can disconnect the model provider.")
		return
	}
	a.renderSettings(r.Context(), w, scope, csrf, "Model provider disconnected. Your saved household records remain available.", "")
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
