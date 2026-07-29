package web

import (
	"errors"
	"html/template"
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

	if !post.Public() {
		account := accountFrom(r.Context())
		if account == nil || (account.ID != post.AccountID && !account.Role.CanModerate()) {
			http.NotFound(w, r)
			return
		}
	}

	w.Header().Set("Vary", "Accept")
	if a.federates && wantsActivityJSON(r) {
		writeActivityJSON(w, http.StatusOK, a.note(post, post.Handle))
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

	branches := a.thread(r.Context(), post.GUID, 0)
	if account := accountFrom(r.Context()); account != nil {
		if flags, err := a.reading.FlagsFor(r.Context(), account.ID, flatten(branches, nil)); err == nil {
			decorateThread(branches, flags)
		}
	}

	mine := false
	if account := accountFrom(r.Context()); account != nil {
		mine = post.AccountID == account.ID || account.Role.CanModerate()
	}

	view := localView(post)
	description := textExcerpt(post.HTML, 160)
	if description == "" {
		description = "A post by " + post.Handle
	}
	ogImage := a.absURL("/assets/mark.png")
	for _, att := range view.Attachments {
		if att.IsImage() {
			ogImage = a.absURL(att.URL)
			break
		}
	}

	a.render(w, r, "post.html", map[string]any{
		"Title":       title + " — RSS Expert",
		"Mine":        mine,
		"Post":        view,
		"Replies":     localViews(replies),
		"Thread":      branches,
		"RepliesURL":  a.posts.RepliesPath(post.ID),
		"Description": description,
		"OGType":      "article",
		"OGImage":     ogImage,
		"ArticleTime": view.PublishedISO,
	})
}

func (a *App) writeForm(w http.ResponseWriter, r *http.Request) {
	inReplyTo := r.URL.Query().Get("to")
	title, source := "", ""

	if account := accountFrom(r.Context()); account != nil {
		if draft, err := a.posts.Draft(r.Context(), account.ID); err == nil {
			if inReplyTo == "" || draft.InReplyTo == inReplyTo {
				title, source, inReplyTo = draft.Title, draft.Markdown, draft.InReplyTo
			}
		}
	}
	a.renderWrite(w, r, inReplyTo, title, source, "")
}

func (a *App) submitWrite(w http.ResponseWriter, r *http.Request) {
	account := accountFrom(r.Context())

	if err := r.ParseForm(); err != nil {
		a.renderWrite(w, r, "", "", "", "That form could not be read.")
		return
	}

	if r.PostFormValue("preview") != "" {
		a.previewWrite(w, r)
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
	visibility := r.PostFormValue("visibility")

	post, err := a.posts.CreateVisible(r.Context(), account, title, source, inReplyTo, visibility)
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
	if projected, err := a.posts.RefreshProjection(r.Context(), post.ID); err != nil {
		a.log.Error("could not refresh the published item after attaching media", "post", post.ID, "error", err)
	} else {
		post = projected
	}
	a.posts.DropDraft(r.Context(), account.ID)
	a.afterPublish(r, post)

	a.log.Info("published", "post", post.ID, "account", account.Email, "reply", post.InReplyTo != "")
	http.Redirect(w, r, "/p/"+strconv.FormatInt(post.ID, 10), http.StatusSeeOther)
}

func (a *App) previewWrite(w http.ResponseWriter, r *http.Request) {
	account := accountFrom(r.Context())

	title := r.PostFormValue("title")
	source := r.PostFormValue("markdown")
	inReplyTo := r.PostFormValue("in_reply_to")

	if err := a.posts.SaveDraft(r.Context(), account.ID, title, source, inReplyTo); err != nil {
		a.log.Error("could not keep a draft", "error", err)
	}

	rendered, err := publish.Render(source)
	if err != nil {
		a.renderWrite(w, r, inReplyTo, title, source, "That Markdown could not be rendered.")
		return
	}

	data := a.writeData(r, inReplyTo, title, source, "")
	data["Preview"] = template.HTML(rendered)
	a.render(w, r, "write.html", data)
}

func (a *App) renderWrite(w http.ResponseWriter, r *http.Request, inReplyTo, title, source, problem string) {
	a.render(w, r, "write.html", a.writeData(r, inReplyTo, title, source, problem))
}

func (a *App) writeData(r *http.Request, inReplyTo, title, source, problem string) map[string]any {
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
	return data
}

func (a *App) editForm(w http.ResponseWriter, r *http.Request) {
	post, ok := a.ownPost(w, r)
	if !ok {
		return
	}

	data := a.writeData(r, post.InReplyTo, post.Title, post.Markdown, "")
	data["Title"] = "Edit — RSS Expert"
	data["Editing"] = localView(post)
	a.render(w, r, "write.html", data)
}

func (a *App) submitEdit(w http.ResponseWriter, r *http.Request) {
	post, ok := a.ownPost(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		http.Error(w, "that form expired", http.StatusBadRequest)
		return
	}

	account := accountFrom(r.Context())
	edited, err := a.posts.Update(r.Context(), account,
		post.ID, r.PostFormValue("title"), r.PostFormValue("markdown"))
	if err != nil {
		data := a.writeData(r, post.InReplyTo, r.PostFormValue("title"), r.PostFormValue("markdown"), err.Error())
		data["Title"] = "Edit — RSS Expert"
		data["Editing"] = localView(post)
		a.render(w, r, "write.html", data)
		return
	}

	a.afterEdit(r, edited)
	a.log.Info("post edited", "post", edited.ID, "account", account.Email)
	http.Redirect(w, r, "/p/"+strconv.FormatInt(edited.ID, 10), http.StatusSeeOther)
}

func (a *App) withdrawPost(w http.ResponseWriter, r *http.Request) {
	post, ok := a.ownPost(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		http.Error(w, "that form expired", http.StatusBadRequest)
		return
	}

	account := accountFrom(r.Context())
	withdrawn, err := a.posts.Delete(r.Context(), account, post.ID)
	if err != nil {
		http.Error(w, "it could not be withdrawn", http.StatusForbidden)
		return
	}

	a.afterWithdraw(r, withdrawn)
	a.log.Info("post withdrawn", "post", withdrawn.ID, "account", account.Email)
	a.render(w, r, "outcome.html", map[string]any{
		"Title":   "Withdrawn — RSS Expert",
		"Heading": "That post is withdrawn",
		"Message": "It is gone from your feed, and everyone following you was told. " +
			"Whoever already read it may still have a copy.",
	})
}

func (a *App) ownPost(w http.ResponseWriter, r *http.Request) (*publish.Post, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}

	post, err := a.posts.ByID(r.Context(), id)
	if err != nil || post.Deleted {
		http.NotFound(w, r)
		return nil, false
	}

	account := accountFrom(r.Context())
	if account == nil || (post.AccountID != account.ID && !account.Role.CanModerate()) {
		http.Error(w, "that post belongs to someone else", http.StatusForbidden)
		return nil, false
	}
	return post, true
}
