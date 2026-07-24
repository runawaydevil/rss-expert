package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/runawaydevil/rss-expert/internal/publish"
)

func (a *App) serveFeed(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Write(body)
}

func (a *App) firehoseFeed(w http.ResponseWriter, r *http.Request) {
	body, err := a.posts.Firehose(r.Context())
	if err != nil {
		a.log.Error("could not build the firehose", "error", err)
		http.Error(w, "could not build the feed", http.StatusInternalServerError)
		return
	}
	a.serveFeed(w, body)
}

func (a *App) accountFeed(w http.ResponseWriter, r *http.Request) {
	body, err := a.posts.AccountFeed(r.Context(), r.PathValue("handle"))
	if err != nil {
		a.log.Error("could not build an account feed", "error", err)
		http.Error(w, "could not build the feed", http.StatusInternalServerError)
		return
	}
	a.serveFeed(w, body)
}

func (a *App) repliesFeed(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	body, err := a.posts.RepliesFeed(r.Context(), id)
	if errors.Is(err, publish.ErrNoPost) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.log.Error("could not build a replies feed", "error", err)
		http.Error(w, "could not build the feed", http.StatusInternalServerError)
		return
	}
	a.serveFeed(w, body)
}

func (a *App) postPage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	post, err := a.posts.ByID(r.Context(), id)
	if errors.Is(err, publish.ErrNoPost) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.log.Error("could not read a post", "error", err)
		http.Error(w, "could not read that post", http.StatusInternalServerError)
		return
	}
	if post.Deleted {
		http.Error(w, "that post was withdrawn", http.StatusGone)
		return
	}

	replies, err := a.posts.Replies(r.Context(), post.GUID, 200)
	if err != nil {
		a.log.Error("could not read replies", "error", err)
	}

	title := post.Title
	if title == "" {
		title = "A post by " + post.Handle
	}

	a.render(w, r, "post.html", map[string]any{
		"Title":      title + " — RSS Expert",
		"Post":       localView(post),
		"Replies":    localViews(replies),
		"RepliesURL": a.posts.RepliesURL(post.ID),
	})
}

func (a *App) writeForm(w http.ResponseWriter, r *http.Request) {
	a.renderWrite(w, r, r.URL.Query().Get("to"), "", "", "")
}

func (a *App) submitWrite(w http.ResponseWriter, r *http.Request) {
	account := accountFrom(r.Context())

	if err := r.ParseForm(); err != nil {
		a.renderWrite(w, r, "", "", "", "That form could not be read.")
		return
	}
	if !validCSRF(r) {
		a.renderWrite(w, r, r.PostFormValue("in_reply_to"), r.PostFormValue("title"),
			r.PostFormValue("markdown"), "That form expired. Try again.")
		return
	}

	title := r.PostFormValue("title")
	source := r.PostFormValue("markdown")
	inReplyTo := r.PostFormValue("in_reply_to")

	post, err := a.posts.Create(r.Context(), account, title, source, inReplyTo)
	if err != nil {
		switch {
		case errors.Is(err, publish.ErrEmptyPost):
			a.renderWrite(w, r, inReplyTo, title, source, "There is nothing in it yet.")
		case errors.Is(err, publish.ErrPostTooLong):
			a.renderWrite(w, r, inReplyTo, title, source, err.Error())
		default:
			a.log.Error("could not publish", "error", err)
			a.renderWrite(w, r, inReplyTo, title, source, "It could not be published. Try again.")
		}
		return
	}

	a.attachToPost(r.Context(), post.ID, r.PostForm["media"])

	a.log.Info("published", "post", post.ID, "account", account.Email, "reply", post.InReplyTo != "")
	http.Redirect(w, r, "/p/"+strconv.FormatInt(post.ID, 10), http.StatusSeeOther)
}

func (a *App) renderWrite(w http.ResponseWriter, r *http.Request, inReplyTo, title, source, problem string) {
	data := map[string]any{
		"Title":     "Write — RSS Expert",
		"InReplyTo": inReplyTo,
		"Draft":     title,
		"Markdown":  source,
		"Problem":   problem,
	}

	if account := accountFrom(r.Context()); account != nil {
		if files, err := a.media.ForAccount(r.Context(), account.ID, 24); err == nil {
			data["Library"] = files
		}
	}

	if inReplyTo != "" {
		if parent, err := a.posts.ByGUID(r.Context(), inReplyTo); err == nil {
			data["Parent"] = localView(parent)
		}
	}
	a.render(w, r, "write.html", data)
}
