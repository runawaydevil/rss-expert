package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/runawaydevil/rss-expert/internal/media"
	"github.com/runawaydevil/rss-expert/internal/micropub"
)

func (a *App) mediaLibrary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := accountFrom(ctx)

	files, err := a.media.ForAccount(ctx, account.ID, 60)
	if err != nil {
		a.log.Error("could not list media", "error", err)
	}
	used, _ := a.media.Used(ctx, account.ID)

	a.render(w, r, "media.html", map[string]any{
		"Title":    "Your files — RSS Expert",
		"Files":    files,
		"UsedMiB":  float64(used) / (1 << 20),
		"QuotaMiB": float64(a.media.Quota()) / (1 << 20),
		"Percent":  int(float64(used) / float64(a.media.Quota()) * 100),
		"Problem":  r.URL.Query().Get("problem"),
	})
}

func (a *App) uploadMedia(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := accountFrom(ctx)

	if err := r.ParseMultipartForm(media.MaxUploadBytes); err != nil {
		a.mediaProblem(w, r, "that upload could not be read")
		return
	}
	if !validCSRF(r) {
		a.mediaProblem(w, r, "that form expired")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		a.mediaProblem(w, r, "no file was attached")
		return
	}
	defer file.Close()

	body, err := io.ReadAll(io.LimitReader(file, media.MaxUploadBytes+1))
	if err != nil {
		a.mediaProblem(w, r, "that file could not be read")
		return
	}

	name := ""
	if header != nil {
		name = header.Filename
	}

	stored, err := a.media.Put(ctx, account.ID, name, body, r.PostFormValue("alt"))
	if err != nil {
		a.log.Info("upload refused", "account", account.Email, "reason", err)
		a.mediaProblem(w, r, err.Error())
		return
	}

	if stored.Stripped {
		a.log.Info("stripped metadata from an upload", "sha", stored.SHA256[:12], "account", account.Email)
	}
	http.Redirect(w, r, "/settings/media", http.StatusSeeOther)
}

func (a *App) mediaProblem(w http.ResponseWriter, r *http.Request, problem string) {
	http.Redirect(w, r, "/settings/media?problem="+urlEscape(problem), http.StatusSeeOther)
}

func (a *App) serveMedia(w http.ResponseWriter, r *http.Request) {
	sum := r.PathValue("sha")

	file, err := a.media.AnyBySHA(r.Context(), sum)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	body, err := a.media.Open(file.SHA256)
	if errors.Is(err, media.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.log.Error("could not open a stored file", "sha", sum[:8], "error", err)
		http.Error(w, "that file could not be read", http.StatusInternalServerError)
		return
	}
	defer body.Close()

	w.Header().Set("Content-Type", file.MediaType)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")

	http.ServeContent(w, r, file.SHA256, file.CreatedAt, body)
}

func (a *App) attachToPost(ctx context.Context, postID int64, ids []string) {
	for position, raw := range ids {
		mediaID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			continue
		}
		if err := a.media.Attach(ctx, postID, mediaID, position); err != nil {
			a.log.Warn("could not attach media to a post", "post", postID, "media", mediaID, "error", err)
		}
	}
}

func urlEscape(s string) string {
	return strings.NewReplacer(" ", "+", "&", "%26", "#", "%23", "?", "%3F").Replace(s)
}

func (a *App) micropubMedia(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	token, err := a.tokens.Resolve(ctx, micropub.BearerToken(r))
	if err != nil {
		micropubError(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return
	}
	if !token.Allows(micropub.ScopeMedia) && !token.Allows(micropub.ScopeCreate) {
		micropubError(w, http.StatusForbidden, "insufficient_scope", "this token cannot upload files")
		return
	}

	if err := r.ParseMultipartForm(media.MaxUploadBytes); err != nil {
		micropubError(w, http.StatusBadRequest, "invalid_request", "that upload could not be read")
		return
	}

	part, header, err := r.FormFile("file")
	if err != nil {
		micropubError(w, http.StatusBadRequest, "invalid_request", "no file was sent")
		return
	}
	defer part.Close()

	body, err := io.ReadAll(io.LimitReader(part, media.MaxUploadBytes+1))
	if err != nil {
		micropubError(w, http.StatusBadRequest, "invalid_request", "that file could not be read")
		return
	}

	name := ""
	if header != nil {
		name = header.Filename
	}

	stored, err := a.media.Put(ctx, token.Account.ID, name, body, r.PostFormValue("alt"))
	if err != nil {
		micropubError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	a.log.Info("file uploaded over micropub", "sha", stored.SHA256[:12], "client", token.ClientID)
	w.Header().Set("Location", a.posts.BaseURL()+stored.URL())
	w.WriteHeader(http.StatusCreated)
}

func (a *App) attachByURL(ctx context.Context, postID, accountID int64, urls []string) {
	base := a.posts.BaseURL()
	position := 0

	for _, raw := range urls {
		sum := strings.TrimPrefix(strings.TrimPrefix(raw, base), "/media/")
		if strings.Contains(sum, "/") {
			continue
		}
		file, err := a.media.BySHA(ctx, accountID, sum)
		if err != nil {
			a.log.Info("a micropub post named a file we do not hold", "url", raw)
			continue
		}
		if err := a.media.Attach(ctx, postID, file.ID, position); err != nil {
			a.log.Warn("could not attach media to a post", "post", postID, "error", err)
			continue
		}
		position++
	}
}
