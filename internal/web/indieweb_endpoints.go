package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/runawaydevil/rss-expert/internal/micropub"
	"github.com/runawaydevil/rss-expert/internal/publish"
)

const maxMicropubBody = 256 << 10

func (a *App) receiveWebmention(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "that request could not be read", http.StatusBadRequest)
		return
	}

	source := r.PostFormValue("source")
	target := r.PostFormValue("target")

	mention, err := a.mentions.Receive(r.Context(), source, target)
	if err != nil {
		a.log.Info("webmention refused", "source", source, "target", target, "reason", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if _, err := a.queue.Enqueue(r.Context(), queueSpec(mention.ID)); err != nil &&
		!errors.Is(err, errDuplicateJob) {
		a.log.Error("could not queue a webmention for verification", "error", err)
	}

	a.log.Info("webmention accepted", "source", source, "target", target)
	w.Header().Set("Location", "/admin")
	w.WriteHeader(http.StatusAccepted)
	io.WriteString(w, "queued for verification\n")
}

func (a *App) micropubEndpoint(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if r.Method == http.MethodGet {
		a.micropubConfig(w, r)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxMicropubBody))
	if err != nil {
		micropubError(w, http.StatusBadRequest, "invalid_request", "the body could not be read")
		return
	}

	var request *micropub.Request
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))

	switch contentType {
	case "application/json":
		request, err = micropub.ParseJSON(body)
	default:
		values, parseErr := url.ParseQuery(string(body))
		if parseErr != nil {
			micropubError(w, http.StatusBadRequest, "invalid_request", "the form could not be read")
			return
		}
		r.Form = values
		request, err = micropub.ParseForm(values)
	}
	if err != nil {
		micropubError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	token, err := a.tokens.Resolve(ctx, micropub.BearerToken(r))
	if err != nil {
		micropubError(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return
	}

	switch request.Action {
	case "create":
		a.micropubCreate(w, r, token, request)
	case "update":
		a.micropubUpdate(w, r, token, request)
	case "delete":
		a.micropubDelete(w, r, token, request)
	default:
		micropubError(w, http.StatusBadRequest, "invalid_request", "unknown action")
	}
}

func (a *App) micropubConfig(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Query().Get("q") {
	case "config":
		writeJSON(w, http.StatusOK, map[string]any{
			"media-endpoint": a.posts.BaseURL() + "/micropub/media",
			"syndicate-to":   []any{},
		})
	case "source":
		post, err := a.posts.ByGUID(r.Context(), r.URL.Query().Get("url"))
		if err != nil {
			micropubError(w, http.StatusNotFound, "not_found", "no such post")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"type": []string{"h-entry"},
			"properties": map[string]any{
				"name":      []string{post.Title},
				"content":   []string{post.Markdown},
				"published": []string{post.Published.Format("2006-01-02T15:04:05Z07:00")},
			},
		})
	default:
		micropubError(w, http.StatusBadRequest, "invalid_request", "unknown query")
	}
}

func (a *App) micropubCreate(w http.ResponseWriter, r *http.Request, token *micropub.Token, request *micropub.Request) {
	if !token.Allows(micropub.ScopeCreate) {
		micropubError(w, http.StatusForbidden, "insufficient_scope", "this token cannot create")
		return
	}

	post, err := a.posts.Create(r.Context(), token.Account, request.Name, request.Content, request.InReplyTo)
	if err != nil {
		if errors.Is(err, publish.ErrEmptyPost) {
			micropubError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		a.log.Error("micropub create failed", "error", err)
		micropubError(w, http.StatusInternalServerError, "server_error", "it could not be published")
		return
	}

	a.attachByURL(r.Context(), post.ID, token.Account.ID, request.Media)

	a.afterPublish(r, post)
	a.log.Info("published over micropub", "post", post.ID, "client", token.ClientID)
	w.Header().Set("Location", post.GUID)
	w.WriteHeader(http.StatusCreated)
}

func (a *App) micropubUpdate(w http.ResponseWriter, r *http.Request, token *micropub.Token, request *micropub.Request) {
	if !token.Allows(micropub.ScopeUpdate) {
		micropubError(w, http.StatusForbidden, "insufficient_scope", "this token cannot update")
		return
	}

	post, err := a.posts.ByGUID(r.Context(), request.URL)
	if err != nil {
		micropubError(w, http.StatusNotFound, "not_found", "no such post")
		return
	}

	title, content := post.Title, post.Markdown
	if values := request.Replace["name"]; len(values) > 0 {
		title = values[0]
	}
	if values := request.Replace["content"]; len(values) > 0 {
		content = values[0]
	}

	updated, err := a.posts.Update(r.Context(), token.Account, post.ID, title, content)
	if err != nil {
		if errors.Is(err, publish.ErrNotYours) {
			micropubError(w, http.StatusForbidden, "forbidden", err.Error())
			return
		}
		a.log.Error("micropub update failed", "error", err)
		micropubError(w, http.StatusInternalServerError, "server_error", "it could not be updated")
		return
	}

	a.afterPublish(r, updated)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) micropubDelete(w http.ResponseWriter, r *http.Request, token *micropub.Token, request *micropub.Request) {
	if !token.Allows(micropub.ScopeDelete) {
		micropubError(w, http.StatusForbidden, "insufficient_scope", "this token cannot delete")
		return
	}

	post, err := a.posts.ByGUID(r.Context(), request.URL)
	if err != nil {
		micropubError(w, http.StatusNotFound, "not_found", "no such post")
		return
	}
	if err := a.posts.Delete(r.Context(), token.Account, post.ID); err != nil {
		micropubError(w, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) journey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	post, err := a.posts.ByID(ctx, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	sent, err := a.mentions.Journey(ctx, post.GUID)
	if err != nil {
		a.log.Error("could not read the webmention journey", "error", err)
	}
	attempts, err := a.ledger.Journey(ctx, post.GUID)
	if err != nil {
		a.log.Error("could not read the delivery ledger", "error", err)
	}

	a.render(w, r, "journey.html", map[string]any{
		"Title":    "Where it went — RSS Expert",
		"Post":     localView(post),
		"Mentions": sent,
		"Attempts": attempts,
		"FeedURL":  "/users/" + post.Handle + "/rss.xml",
	})
}

func micropubError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]string{
		"error":             code,
		"error_description": description,
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}
