package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/runawaydevil/rss-expert/internal/micropub"
)

var grantableScopes = []string{
	micropub.ScopeCreate,
	micropub.ScopeUpdate,
	micropub.ScopeDelete,
	micropub.ScopeMedia,
}

func (a *App) tokenSettings(w http.ResponseWriter, r *http.Request) {
	a.renderTokens(w, r, "", "")
}

func (a *App) renderTokens(w http.ResponseWriter, r *http.Request, fresh, problem string) {
	account := accountFrom(r.Context())

	issued, err := a.tokens.ForAccount(r.Context(), account.ID)
	if err != nil {
		a.log.Error("could not list tokens", "error", err)
	}

	a.render(w, r, "tokens.html", map[string]any{
		"Title":   "App passwords — RSS Expert",
		"Tokens":  issued,
		"Scopes":  grantableScopes,
		"Fresh":   fresh,
		"Problem": problem,
	})
}

func (a *App) issueToken(w http.ResponseWriter, r *http.Request) {
	account := accountFrom(r.Context())

	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		a.renderTokens(w, r, "", "That form expired. Try again.")
		return
	}

	client := strings.TrimSpace(r.PostFormValue("client"))
	if client == "" {
		a.renderTokens(w, r, "", "Give it a name, so you know which app to revoke later.")
		return
	}
	if len(client) > 200 {
		client = client[:200]
	}

	var granted []string
	for _, scope := range grantableScopes {
		for _, asked := range r.PostForm["scope"] {
			if asked == scope {
				granted = append(granted, scope)
			}
		}
	}
	if len(granted) == 0 {
		a.renderTokens(w, r, "", "Choose at least one thing the app may do.")
		return
	}

	token, err := a.tokens.Issue(r.Context(), account, client, strings.Join(granted, " "))
	if err != nil {
		a.log.Error("could not issue a token", "error", err)
		a.renderTokens(w, r, "", "It could not be issued. Try again.")
		return
	}

	a.log.Info("issued an app password", "account", account.Email, "client", client, "scope", strings.Join(granted, " "))
	a.renderTokens(w, r, token, "")
}

func (a *App) revokeToken(w http.ResponseWriter, r *http.Request) {
	account := accountFrom(r.Context())

	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		a.renderTokens(w, r, "", "That form expired. Try again.")
		return
	}

	id, err := strconv.ParseInt(r.PostFormValue("token"), 10, 64)
	if err != nil {
		a.renderTokens(w, r, "", "No such app password.")
		return
	}
	if err := a.tokens.Revoke(r.Context(), account.ID, id); err != nil {
		a.log.Error("could not revoke a token", "error", err)
		a.renderTokens(w, r, "", "It could not be revoked. Try again.")
		return
	}

	a.log.Info("revoked an app password", "account", account.Email, "token", id)
	http.Redirect(w, r, "/settings/tokens", http.StatusSeeOther)
}
