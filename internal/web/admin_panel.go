package web

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/moderation"
)

const ReauthWindow = 10 * time.Minute

func (a *App) requireModerator(next http.HandlerFunc) http.HandlerFunc {
	return a.auth.requireAccount(func(w http.ResponseWriter, r *http.Request) {
		if account := accountFrom(r.Context()); !account.Role.CanModerate() {
			http.Error(w, "that page is for moderators", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}

func (a *App) requireFreshLogin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		fresh, err := a.accounts.ReauthenticatedWithin(r.Context(), cookie.Value, ReauthWindow)
		if err != nil {
			a.log.Error("could not check reauthentication", "error", err)
		}
		if !fresh {
			http.Redirect(w, r, "/admin/confirm?next="+url.QueryEscape(r.URL.Path), http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func (a *App) adminPanel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := accountFrom(ctx)

	reports, err := a.moderation.OpenReports(ctx, 50)
	if err != nil {
		a.log.Error("could not read reports", "error", err)
	}
	blocks, err := a.moderation.Blocks(ctx, account.ID)
	if err != nil {
		a.log.Error("could not read blocks", "error", err)
	}
	audit, err := a.moderation.Audit(ctx, 40)
	if err != nil {
		a.log.Error("could not read the audit log", "error", err)
	}
	depth, err := a.queue.Depth(ctx)
	if err != nil {
		a.log.Error("could not read queue depth", "error", err)
	}
	failing, err := a.ledger.Failing(ctx, time.Now().Add(-24*time.Hour), 20)
	if err != nil {
		a.log.Error("could not read delivery failures", "error", err)
	}
	payloads, observations, items, err := a.sources.Counts(ctx)
	if err != nil {
		a.log.Error("could not count the store", "error", err)
	}
	size, _ := a.db.SizeOnDisk()

	twoFactor, _ := a.accounts.TOTPEnabled(ctx, account.ID)
	codesLeft, _ := a.accounts.RecoveryCodesLeft(ctx, account.ID)

	a.render(w, r, "admin.html", map[string]any{
		"Title":        "Administration — RSS Expert",
		"Reports":      reports,
		"Blocks":       blocks,
		"Audit":        audit,
		"Queue":        depth,
		"Failing":      failing,
		"Payloads":     payloads,
		"Observations": observations,
		"Items":        items,
		"DatabaseMiB":  float64(size) / (1 << 20),
		"TwoFactor":    twoFactor,
		"CodesLeft":    codesLeft,
	})
}

func (a *App) decideReport(w http.ResponseWriter, r *http.Request) {
	account := accountFrom(r.Context())

	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		http.Error(w, "that form expired", http.StatusBadRequest)
		return
	}

	id, err := strconv.ParseInt(r.PostFormValue("report"), 10, 64)
	if err != nil {
		http.Error(w, "which report?", http.StatusBadRequest)
		return
	}

	upheld := r.PostFormValue("decision") == "uphold"
	if err := a.moderation.Decide(r.Context(), account, id, upheld, r.PostFormValue("note")); err != nil {
		if errors.Is(err, moderation.ErrNoReport) {
			http.Error(w, "that report was already decided", http.StatusConflict)
			return
		}
		a.log.Error("could not decide a report", "error", err)
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (a *App) blockSomething(w http.ResponseWriter, r *http.Request) {
	account := accountFrom(r.Context())

	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		http.Error(w, "that form expired", http.StatusBadRequest)
		return
	}

	kind := moderation.Kind(r.PostFormValue("kind"))
	value := r.PostFormValue("value")
	reason := r.PostFormValue("reason")

	var scope int64
	if r.PostFormValue("scope") == "mine" {
		scope = account.ID
	}

	var err error
	if r.PostFormValue("action") == "unblock" {
		err = a.moderation.Unblock(r.Context(), account, scope, kind, value)
	} else {
		err = a.moderation.Block(r.Context(), account, scope, kind, value, reason)
	}
	if err != nil {
		a.log.Warn("could not change a block", "error", err)
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (a *App) retryJob(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		http.Error(w, "that form expired", http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(r.PostFormValue("job"), 10, 64)
	if err != nil {
		http.Error(w, "which job?", http.StatusBadRequest)
		return
	}
	if err := a.queue.Retry(r.Context(), id); err != nil {
		a.log.Warn("could not retry a job", "job", id, "error", err)
	}
	a.moderation.Log(r.Context(), accountFrom(r.Context()), "job.retry", strconv.FormatInt(id, 10), "")
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (a *App) confirmPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "confirm.html", map[string]any{
		"Title":   "Confirm it is you — RSS Expert",
		"Next":    safeNext(r.URL.Query().Get("next")),
		"Problem": "",
	})
}

func (a *App) confirmIdentity(w http.ResponseWriter, r *http.Request) {
	account := accountFrom(r.Context())

	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		http.Error(w, "that form expired", http.StatusBadRequest)
		return
	}

	next := safeNext(r.PostFormValue("next"))
	problem := ""

	if _, err := a.accounts.Authenticate(r.Context(), account.Email, r.PostFormValue("password")); err != nil {
		problem = "That password is not right."
	} else if on, _ := a.accounts.TOTPEnabled(r.Context(), account.ID); on {
		if err := a.accounts.CheckSecondFactor(r.Context(), account, r.PostFormValue("code")); err != nil {
			problem = "That two-factor code is not right."
		}
	}

	if problem != "" {
		a.render(w, r, "confirm.html", map[string]any{
			"Title": "Confirm it is you — RSS Expert", "Next": next, "Problem": problem,
		})
		return
	}

	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if err := a.accounts.MarkReauthenticated(r.Context(), cookie.Value); err != nil {
			a.log.Error("could not record reauthentication", "error", err)
		}
	}
	a.moderation.Log(r.Context(), account, "account.reauthenticate", next, "")
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (a *App) twoFactorPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := accountFrom(ctx)

	on, err := a.accounts.TOTPEnabled(ctx, account.ID)
	if err != nil {
		a.log.Error("could not read two-factor state", "error", err)
	}

	data := map[string]any{
		"Title":    "Two-factor — RSS Expert",
		"Enabled":  on,
		"Required": account.Role.CanAdminister(),
	}
	if on {
		left, _ := a.accounts.RecoveryCodesLeft(ctx, account.ID)
		data["CodesLeft"] = left
	} else {
		secret, err := a.accounts.BeginTOTP(ctx, account)
		if err != nil {
			a.log.Error("could not start two-factor enrolment", "error", err)
		}
		data["Secret"] = secret
		data["URI"] = template.URL(identity.TOTPURI("RSS Expert", account.Email, secret))
	}
	a.render(w, r, "twofactor.html", data)
}

func (a *App) enableTwoFactor(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := accountFrom(ctx)

	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		http.Error(w, "that form expired", http.StatusBadRequest)
		return
	}

	codes, err := a.accounts.ConfirmTOTP(ctx, account, r.PostFormValue("code"))
	if err != nil {
		secret, _ := a.accounts.BeginTOTP(ctx, account)
		a.render(w, r, "twofactor.html", map[string]any{
			"Title": "Two-factor — RSS Expert", "Enabled": false,
			"Required": account.Role.CanAdminister(),
			"Secret":   secret,
			"URI":      template.URL(identity.TOTPURI("RSS Expert", account.Email, secret)),
			"Problem":  "That code is not right. Check your authenticator and try again.",
		})
		return
	}

	a.moderation.Log(ctx, account, "account.twofactor.enable", account.Email, "")
	a.render(w, r, "twofactor.html", map[string]any{
		"Title": "Two-factor — RSS Expert", "Enabled": true,
		"Required": account.Role.CanAdminister(),
		"Codes":    codes, "CodesLeft": len(codes),
	})
}

func (a *App) disableTwoFactor(w http.ResponseWriter, r *http.Request) {
	account := accountFrom(r.Context())

	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		http.Error(w, "that form expired", http.StatusBadRequest)
		return
	}
	if account.Role.CanAdminister() {
		http.Error(w, "administrators cannot switch two-factor off", http.StatusForbidden)
		return
	}
	if err := a.accounts.DisableTOTP(r.Context(), account); err != nil {
		a.log.Error("could not disable two-factor", "error", err)
	}
	a.moderation.Log(r.Context(), account, "account.twofactor.disable", account.Email, "")
	http.Redirect(w, r, "/settings/two-factor", http.StatusSeeOther)
}

func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/admin"
	}
	if strings.HasPrefix(next, `/\`) || strings.ContainsAny(next, "\r\n") {
		return "/admin"
	}
	return next
}
