package web

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/mail"
)

func (a *App) registerForm(w http.ResponseWriter, r *http.Request) {
	email := r.URL.Query().Get("email")
	if a.registration == "closed" && email == "" {
		a.render(w, r, "register.html", map[string]any{
			"Title":  "Create an account — RSS Expert",
			"Closed": true,
		})
		return
	}
	a.renderRegister(w, r, email, "", false)
}

func (a *App) submitRegister(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		a.renderRegister(w, r, "", "That form expired. Try again.", false)
		return
	}
	if !a.auth.attempts.allow(clientIP(r, a.behindProxy)) {
		a.renderRegister(w, r, r.PostFormValue("email"), "Too many attempts from this address. Wait a few minutes.", false)
		return
	}

	email := strings.TrimSpace(r.PostFormValue("email"))
	password := r.PostFormValue("password")
	invite := strings.TrimSpace(r.PostFormValue("invite"))

	var (
		account *identity.Account
		err     error
	)
	if a.registration == "open" {
		account, err = a.accounts.Create(ctx, email, password, identity.RoleUser)
	} else {
		account, err = a.accounts.CreateWithInvite(ctx, email, password, identity.RoleUser, invite)
	}

	switch {
	case a.registration != "open" &&
		(errors.Is(err, identity.ErrBadToken) ||
			errors.Is(err, identity.ErrTokenExpired) ||
			errors.Is(err, identity.ErrTokenUsed)):
		a.renderRegister(w, r, email, "That invitation is not valid for this address.", true)
		return
	case errors.Is(err, identity.ErrEmailTaken):
		a.log.Info("someone tried to register a taken address", "email", email)
		a.renderRegister(w, r, email, "If that address is free, your account is created. Check your mail.", false)
		return
	case errors.Is(err, identity.ErrPasswordTooShort):
		a.renderRegister(w, r, email, err.Error(), a.registration != "open")
		return
	case errors.Is(err, identity.ErrEmailUnusable):
		a.renderRegister(w, r, email, "That does not look like an email address.", a.registration != "open")
		return
	case err != nil:
		a.log.Error("could not create an account", "error", err)
		a.renderRegister(w, r, email, "It could not be created. Try again.", a.registration != "open")
		return
	}

	a.sendVerification(ctx, account)
	a.log.Info("account registered", "email", account.Email, "mode", a.registration)
	a.render(w, r, "sent.html", map[string]any{
		"Title": "Check your mail — RSS Expert",
		"What":  "verify",
		"Email": account.Email,
	})
}

func (a *App) sendVerification(ctx context.Context, account *identity.Account) {
	token, err := a.accounts.IssueToken(ctx, account.ID, account.Email, identity.PurposeVerify)
	if err != nil {
		a.log.Error("could not issue a verification token", "error", err)
		return
	}
	a.mailLink(ctx, account.Email, "Confirm your account",
		"Someone created an account with this address on "+a.host()+".\n\n"+
			"Confirm it by opening this link. It works once and expires in two hours:\n\n"+
			a.posts.BaseURL()+"/account/verify?token="+token+"\n\n"+
			"If this was not you, ignore this message and nothing happens.")
}

func (a *App) verifyEmail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	token, err := a.accounts.RedeemToken(ctx, r.URL.Query().Get("token"), identity.PurposeVerify)
	if err != nil {
		a.render(w, r, "outcome.html", map[string]any{
			"Title":   "That link did not work — RSS Expert",
			"Heading": "That link did not work",
			"Message": friendlyToken(err),
		})
		return
	}

	if err := a.accounts.MarkEmailVerified(ctx, token.AccountID); err != nil {
		a.log.Error("could not mark an email verified", "error", err)
	}

	a.startSession(w, r, token.AccountID, "verified and signed in")
}

func (a *App) magicForm(w http.ResponseWriter, r *http.Request) {
	a.renderMagic(w, r, "", "")
}

func (a *App) submitMagic(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		a.renderMagic(w, r, "", "That form expired. Try again.")
		return
	}
	if !a.auth.attempts.allow(clientIP(r, a.behindProxy)) {
		a.renderMagic(w, r, r.PostFormValue("email"), "Too many attempts from this address. Wait a few minutes.")
		return
	}

	email := strings.TrimSpace(r.PostFormValue("email"))
	purpose := identity.PurposeSignIn
	if r.PostFormValue("recover") != "" {
		purpose = identity.PurposeRecover
	}

	if account, err := a.accounts.ByEmail(ctx, email); err == nil {
		token, err := a.accounts.IssueToken(ctx, account.ID, account.Email, purpose)
		if err != nil {
			a.log.Error("could not issue a sign-in token", "error", err)
		} else {
			a.sendMagic(ctx, account.Email, purpose, token)
		}
	} else {
		a.log.Info("a link was asked for an address with no account", "email", email)
	}

	a.render(w, r, "sent.html", map[string]any{
		"Title": "Check your mail — RSS Expert",
		"What":  string(purpose),
		"Email": email,
	})
}

func (a *App) sendMagic(ctx context.Context, email, purpose, token string) {
	if purpose == identity.PurposeRecover {
		a.mailLink(ctx, email, "Set a new password",
			"Open this link to set a new password on "+a.host()+". It works once and expires in two hours:\n\n"+
				a.posts.BaseURL()+"/account/recover?token="+token+"\n\n"+
				"If you did not ask for this, ignore it — your password is unchanged.")
		return
	}
	a.mailLink(ctx, email, "Your sign-in link",
		"Open this link to sign in to "+a.host()+". It works once and expires in two hours:\n\n"+
			a.posts.BaseURL()+"/account/link?token="+token+"\n\n"+
			"If you did not ask for this, ignore it.")
}

func (a *App) followMagic(w http.ResponseWriter, r *http.Request) {
	token, err := a.accounts.RedeemToken(r.Context(), r.URL.Query().Get("token"), identity.PurposeSignIn)
	if err != nil {
		a.render(w, r, "outcome.html", map[string]any{
			"Title":   "That link did not work — RSS Expert",
			"Heading": "That link did not work",
			"Message": friendlyToken(err),
		})
		return
	}
	a.accounts.MarkEmailVerified(r.Context(), token.AccountID)
	a.startSession(w, r, token.AccountID, "signed in")
}

func (a *App) recoverForm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if _, err := a.accounts.PeekToken(r.Context(), token, identity.PurposeRecover); err != nil {
		a.render(w, r, "outcome.html", map[string]any{
			"Title":   "That link did not work — RSS Expert",
			"Heading": "That link did not work",
			"Message": friendlyToken(err),
		})
		return
	}

	a.render(w, r, "recover.html", map[string]any{
		"Title":   "Set a new password — RSS Expert",
		"Token":   token,
		"Problem": "",
	})
}

func (a *App) submitRecover(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		http.Redirect(w, r, "/account/forgot", http.StatusSeeOther)
		return
	}

	token := r.PostFormValue("token")
	accountID, err := a.accounts.RecoverPassword(ctx, token, r.PostFormValue("password"))
	if errors.Is(err, identity.ErrBadToken) ||
		errors.Is(err, identity.ErrTokenExpired) ||
		errors.Is(err, identity.ErrTokenUsed) {
		a.render(w, r, "outcome.html", map[string]any{
			"Title":   "That link did not work — RSS Expert",
			"Heading": "That link did not work",
			"Message": friendlyToken(err),
		})
		return
	}
	if err != nil {
		a.render(w, r, "recover.html", map[string]any{
			"Title":   "Set a new password — RSS Expert",
			"Token":   token,
			"Problem": err.Error(),
		})
		return
	}

	a.log.Info("password set through recovery", "account", accountID)
	a.startSession(w, r, accountID, "your new password is set and you are signed in")
}

func (a *App) startSession(w http.ResponseWriter, r *http.Request, accountID int64, note string) {
	account, err := a.accounts.ByID(r.Context(), accountID)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	token, expires, err := a.accounts.CreateSession(r.Context(), account)
	if err != nil {
		a.log.Error("could not create a session", "error", err)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   overTLS(r, a.behindProxy),
		SameSite: http.SameSiteLaxMode,
	})
	a.log.Info("session started", "account", account.Email, "how", note)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) mailLink(ctx context.Context, to, subject, body string) {
	if a.mail == nil {
		a.log.Warn("an email would have been sent but no server is configured",
			"to", to, "subject", subject)
		return
	}
	go func() {
		if err := a.mail.Send(context.WithoutCancel(ctx), mail.Message{To: to, Subject: subject, Body: body}); err != nil {
			a.log.Error("could not send an email", "to", to, "error", err)
		}
	}()
}

func (a *App) renderRegister(w http.ResponseWriter, r *http.Request, email, problem string, keepInvite bool) {
	data := map[string]any{
		"Title":   "Create an account — RSS Expert",
		"Email":   email,
		"Problem": problem,
		"Invite":  a.registration != "open",
	}
	if keepInvite {
		data["InviteToken"] = r.PostFormValue("invite")
	}
	a.render(w, r, "register.html", data)
}

func (a *App) renderMagic(w http.ResponseWriter, r *http.Request, email, problem string) {
	a.render(w, r, "magic.html", map[string]any{
		"Title":   "Sign in by email — RSS Expert",
		"Email":   email,
		"Problem": problem,
	})
}

func friendlyToken(err error) string {
	switch {
	case errors.Is(err, identity.ErrTokenExpired):
		return "That link has expired. Ask for a fresh one."
	case errors.Is(err, identity.ErrTokenUsed):
		return "That link was already used. Ask for a fresh one if you need it."
	default:
		return "That link is not valid. Ask for a fresh one."
	}
}

func (a *App) inviteForm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}

	account := accountFrom(r.Context())
	email := strings.TrimSpace(r.PostFormValue("email"))
	if email == "" {
		http.Redirect(w, r, "/admin?invite=missing", http.StatusSeeOther)
		return
	}

	token, err := a.accounts.IssueToken(r.Context(), account.ID, email, identity.PurposeInvite)
	if err != nil {
		a.log.Error("could not issue an invitation", "error", err)
		http.Redirect(w, r, "/admin?invite=failed", http.StatusSeeOther)
		return
	}

	link := a.posts.BaseURL() + "/register?email=" + urlEscape(email)
	if a.mail != nil {
		a.mailLink(r.Context(), email, "You are invited to "+a.host(),
			"Someone invited you to open an account on "+a.host()+".\n\n"+
				"Start here, and enter this code when asked:\n\n"+link+"\n\nInvitation code: "+token+"\n\n"+
				"The invitation is good for a week.")
		a.log.Info("invitation sent", "email", email, "by", account.Email)
		http.Redirect(w, r, "/admin?invite=sent", http.StatusSeeOther)
		return
	}

	a.log.Info("invitation issued", "email", email, "by", account.Email)
	http.Redirect(w, r, "/admin?invite="+urlEscape(token), http.StatusSeeOther)
}
