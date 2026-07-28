package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/runawaydevil/rss-expert/internal/identity"
)

const (
	sessionCookieName = "rss_expert_session"
	csrfCookieName    = "rss_expert_csrf"
	csrfField         = "csrf"
)

type contextKey int

const accountContextKey contextKey = iota

type auth struct {
	accounts    *identity.Store
	log         *slog.Logger
	attempts    *limiter
	behindProxy bool
}

func newAuth(accounts *identity.Store, log *slog.Logger, behindProxy bool) *auth {
	return &auth{
		accounts:    accounts,
		log:         log,
		attempts:    newLimiter(8, 20),
		behindProxy: behindProxy,
	}
}

func accountFrom(ctx context.Context) *identity.Account {
	account, _ := ctx.Value(accountContextKey).(*identity.Account)
	return account
}

func (a *auth) resolve(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			next.ServeHTTP(w, r)
			return
		}

		account, err := a.accounts.Session(r.Context(), cookie.Value)
		if err != nil {
			if !errors.Is(err, identity.ErrNoSession) && !errors.Is(err, identity.ErrAccountDisabled) {
				a.log.Error("could not resolve session", "error", err)
			}
			clearCookie(w, r, sessionCookieName, a.behindProxy)
			next.ServeHTTP(w, r)
			return
		}

		ctx := context.WithValue(r.Context(), accountContextKey, account)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *auth) requireAccount(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if accountFrom(r.Context()) == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func (a *auth) login(w http.ResponseWriter, r *http.Request) {
	if accountFrom(r.Context()) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.renderLogin(w, r, "", "")
}

func (a *auth) submitLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		a.renderLogin(w, r, "", "That form could not be read.")
		return
	}
	if !validCSRF(r) {
		a.renderLogin(w, r, "", "That form expired. Try again.")
		return
	}

	email := r.PostFormValue("email")

	if !a.attempts.allow(clientIP(r, a.behindProxy)) {
		a.log.Warn("login rate limit reached", "ip", clientIP(r, a.behindProxy))
		w.WriteHeader(http.StatusTooManyRequests)
		a.renderLogin(w, r, email, "Too many attempts from this address. Wait a few minutes.")
		return
	}

	account, err := a.accounts.Authenticate(r.Context(), email, r.PostFormValue("password"))
	if err != nil {
		switch {
		case errors.Is(err, identity.ErrBadCredentials), errors.Is(err, identity.ErrAccountDisabled):
			a.log.Info("failed sign-in", "email", email, "ip", clientIP(r, a.behindProxy))
			a.renderLogin(w, r, email, "That email and password do not match an account.")
		default:
			a.log.Error("sign-in failed unexpectedly", "error", err)
			a.renderLogin(w, r, email, "Something went wrong. Try again.")
		}
		return
	}

	twoFactor, err := a.accounts.TOTPEnabled(r.Context(), account.ID)
	if err != nil {
		a.log.Error("could not read two-factor state during sign-in", "account", account.Email, "error", err)
		a.renderLogin(w, r, email, "Something went wrong. Try again.")
		return
	}
	if twoFactor {
		if err := a.accounts.CheckSecondFactor(r.Context(), account, r.PostFormValue("code")); err != nil {
			a.log.Info("failed second factor", "email", email, "ip", clientIP(r, a.behindProxy))
			a.renderLogin(w, r, email, "That two-factor or recovery code is not right.")
			return
		}
	}

	token, expires, err := a.accounts.CreateSession(r.Context(), account)
	if err != nil {
		a.log.Error("could not create session", "error", err)
		a.renderLogin(w, r, email, "Signed in, but the session could not be stored. Try again.")
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
	a.log.Info("signed in", "account", account.Email, "role", account.Role)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *auth) logout(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err == nil && validCSRF(r) {
		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			a.accounts.DestroySession(r.Context(), cookie.Value)
		}
	}
	clearCookie(w, r, sessionCookieName, a.behindProxy)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *auth) renderLogin(w http.ResponseWriter, r *http.Request, email, problem string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := map[string]any{
		"Title":   "Sign in — RSS Expert",
		"CSRF":    csrfToken(w, r, a.behindProxy),
		"Email":   email,
		"Problem": problem,
	}
	if err := templates.ExecuteTemplate(w, "login.html", data); err != nil {
		a.log.Error("login page render failed", "error", err)
	}
}

func csrfToken(w http.ResponseWriter, r *http.Request, behindProxy bool) string {
	if cookie, err := r.Cookie(csrfCookieName); err == nil && len(cookie.Value) >= 20 {
		return cookie.Value
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return ""
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		Expires:  time.Now().Add(12 * time.Hour),
		HttpOnly: true,
		Secure:   overTLS(r, behindProxy),
		SameSite: http.SameSiteLaxMode,
	})
	return token
}

func validCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	sent := r.PostFormValue(csrfField)
	if sent == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(sent)) == 1
}

func clearCookie(w http.ResponseWriter, r *http.Request, name string, behindProxy bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   overTLS(r, behindProxy),
		SameSite: http.SameSiteLaxMode,
	})
}

func overTLS(r *http.Request, behindProxy bool) bool {
	if r.TLS != nil {
		return true
	}
	return behindProxy && r.Header.Get("X-Forwarded-Proto") == "https"
}
