package app

import (
	"context"
	"net/http"
	"strings"

	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/providers"
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

func (a *App) saveOpenAISetting(w http.ResponseWriter, r *http.Request, scope policy.ActorScope, csrf string) {
	apiKey := r.PostForm.Get("api_key")
	err := a.providerSettings.ReplaceOpenAI(r.Context(), scope, apiKey, func(ctx context.Context, candidate string) error {
		client, err := providers.NewOpenAI(providers.OpenAIConfig{APIKey: candidate, Client: a.openAIClient})
		if err != nil {
			return err
		}
		return client.Validate(ctx)
	})
	if err != nil {
		a.renderSettings(r.Context(), w, scope, csrf, "", "Mithra could not verify that key right now. Your existing connection is unchanged. Check the key or try again later.")
		return
	}
	a.renderSettings(r.Context(), w, scope, csrf, "OpenAI is connected. Mithra never displays the saved key.", "")
}

func (a *App) removeOpenAISetting(w http.ResponseWriter, r *http.Request, scope policy.ActorScope, csrf string) {
	if err := a.providerSettings.RemoveOpenAI(r.Context(), scope); err != nil {
		a.renderSettings(r.Context(), w, scope, csrf, "", "Only the household owner can disconnect OpenAI.")
		return
	}
	a.renderSettings(r.Context(), w, scope, csrf, "OpenAI was disconnected. Your saved household records remain available.", "")
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
