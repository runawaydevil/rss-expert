package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/runawaydevil/rss-expert/internal/activitypub"
	"github.com/runawaydevil/rss-expert/internal/feed"
	"github.com/runawaydevil/rss-expert/internal/publish"
)

const inboxMaxBytes = 1 << 20

func (a *App) actorURI(handle string) string {
	return a.posts.BaseURL() + "/users/" + handle
}

func (a *App) keyID(handle string) string {
	return a.actorURI(handle) + "#main-key"
}

func wantsActivityJSON(r *http.Request) bool {
	accept := strings.ToLower(r.Header.Get("Accept"))
	if accept == "" {
		return false
	}
	if strings.Contains(accept, "text/html") {
		return false
	}
	return strings.Contains(accept, "activity+json") ||
		strings.Contains(accept, "ld+json") ||
		strings.Contains(accept, `profile="https://www.w3.org/ns/activitystreams"`)
}

func writeActivityJSON(w http.ResponseWriter, status int, document any) {
	w.Header().Set("Content-Type", activitypub.ContentType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(document)
}

func (a *App) webfinger(w http.ResponseWriter, r *http.Request) {
	address, err := activitypub.ParseResource(r.URL.Query().Get("resource"))
	if err != nil {
		http.Error(w, "resource must be an acct: address", http.StatusBadRequest)
		return
	}
	if !strings.EqualFold(address.Host, a.host()) {
		http.Error(w, "no such account here", http.StatusNotFound)
		return
	}

	accountID, handle, err := a.posts.AccountByHandle(r.Context(), address.User)
	if err != nil || accountID == 0 {
		http.Error(w, "no such account here", http.StatusNotFound)
		return
	}
	if _, err := a.ap.PublicKeyFor(r.Context(), accountID); err != nil {
		a.log.Error("could not prepare an actor key", "error", err)
		http.Error(w, "no such account here", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", activitypub.JRDType)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_ = json.NewEncoder(w).Encode(activitypub.Descriptor(
		activitypub.Address{User: handle, Host: a.host()},
		a.actorURI(handle), a.actorURI(handle)))
}

func (a *App) actorFor(ctx context.Context, handle string) (*activitypub.Actor, error) {
	accountID, canonical, err := a.posts.AccountByHandle(ctx, handle)
	if err != nil || accountID == 0 {
		return nil, errNoSuchAccount
	}

	public, err := a.ap.PublicKeyFor(ctx, accountID)
	if err != nil {
		return nil, err
	}

	uri := a.actorURI(canonical)
	actor := &activitypub.Actor{
		Context:           activitypub.Context(),
		ID:                uri,
		Type:              "Person",
		PreferredUsername: canonical,
		Name:              canonical,
		URL:               uri,
		Inbox:             uri + "/inbox",
		Outbox:            uri + "/outbox",
		Followers:         uri + "/followers",
		Following:         uri + "/following",
		Endpoints:         &activitypub.Endpoints{SharedInbox: a.posts.BaseURL() + "/inbox"},
		Discoverable:      true,
		PublicKey: &activitypub.PublicKey{
			ID:           a.keyID(canonical),
			Owner:        uri,
			PublicKeyPEM: public,
		},
	}

	if sites, err := a.sites.ForAccount(ctx, accountID); err == nil {
		for _, site := range sites {
			if !site.Verified() {
				continue
			}
			if site.Name != "" {
				actor.Name = site.Name
			}
			actor.Summary = site.Note
			if actor.Summary == "" {
				actor.Summary = site.URL
			}
			break
		}
	}
	return actor, nil
}

var errNoSuchAccount = errors.New("web: no such account")

func (a *App) actorJSON(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actorFor(r.Context(), r.PathValue("handle"))
	if err != nil {
		http.Error(w, "no such account", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeActivityJSON(w, http.StatusOK, actor)
}

type orderedCollection struct {
	Context    any    `json:"@context"`
	ID         string `json:"id"`
	Type       string `json:"type"`
	TotalItems int    `json:"totalItems"`
	First      string `json:"first,omitempty"`
	Items      []any  `json:"orderedItems"`
}

func (a *App) followersCollection(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	accountID, canonical, err := a.posts.AccountByHandle(r.Context(), handle)
	if err != nil || accountID == 0 {
		http.Error(w, "no such account", http.StatusNotFound)
		return
	}

	total, err := a.ap.CountFollowers(r.Context(), accountID)
	if err != nil {
		a.log.Error("could not count followers", "error", err)
	}

	writeActivityJSON(w, http.StatusOK, orderedCollection{
		Context:    "https://www.w3.org/ns/activitystreams",
		ID:         a.actorURI(canonical) + "/followers",
		Type:       "OrderedCollection",
		TotalItems: total,
		Items:      []any{},
	})
}

func (a *App) outboxCollection(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	accountID, canonical, err := a.posts.AccountByHandle(r.Context(), handle)
	if err != nil || accountID == 0 {
		http.Error(w, "no such account", http.StatusNotFound)
		return
	}

	posts, err := a.posts.ByHandle(r.Context(), canonical, publish.FeedItemLimit)
	if err != nil {
		a.log.Error("could not read an account's posts", "error", err)
	}
	total, err := a.posts.CountByHandle(r.Context(), canonical)
	if err != nil {
		a.log.Error("could not count an account's outbox", "error", err)
	}
	items := make([]any, 0, len(posts))
	for _, post := range posts {
		items = append(items, a.createActivity(post))
	}

	writeActivityJSON(w, http.StatusOK, orderedCollection{
		Context:    "https://www.w3.org/ns/activitystreams",
		ID:         a.actorURI(canonical) + "/outbox",
		Type:       "OrderedCollection",
		TotalItems: total,
		Items:      items,
	})
}

func (a *App) inbox(w http.ResponseWriter, r *http.Request) {
	if !a.inboxLimit.allow(clientIP(r, a.behindProxy)) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}

	handle := r.PathValue("handle")
	accountID, canonical, err := a.posts.AccountByHandle(r.Context(), handle)
	if err != nil || accountID == 0 {
		http.Error(w, "no such account", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, inboxMaxBytes))
	if err != nil {
		http.Error(w, "that delivery is too large", http.StatusRequestEntityTooLarge)
		return
	}

	activity, actor, err := a.authenticateDelivery(r, body)
	if err != nil {
		a.log.Warn("refused an inbox delivery",
			"error", err, "ip", clientIP(r, a.behindProxy), "handle", canonical)
		http.Error(w, "that delivery could not be authenticated", http.StatusUnauthorized)
		return
	}

	if a.ap.AlreadySeen(r.Context(), activity.ID) {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	a.dispatch(w, r, accountID, canonical, activity, actor)
}

func (a *App) dispatch(w http.ResponseWriter, r *http.Request, accountID int64, handle string,
	activity *activitypub.Activity, actor *activitypub.Actor) {

	switch activity.Type {
	case "Follow":
		if accountID == 0 {
			http.Error(w, "that follow names nobody here", http.StatusBadRequest)
			return
		}
		a.acceptFollow(w, r, accountID, handle, activity, actor)
	case "Create":
		a.acceptCreate(w, r, activity, actor)
	case "Undo":
		a.undo(w, r, accountID, activity, actor)
	case "Like", "Announce":
		a.acceptReaction(w, r, activity, actor)
	case "Delete":
		w.WriteHeader(http.StatusAccepted)
	default:
		a.log.Info("ignored an activity we do not handle yet",
			"type", activity.Type, "actor", actor.ID)
		w.WriteHeader(http.StatusAccepted)
	}
}

func (a *App) acceptCreate(w http.ResponseWriter, r *http.Request,
	activity *activitypub.Activity, actor *activitypub.Actor) {

	note, err := activity.Note()
	if err != nil || note.Type != "Note" || note.InReplyTo == "" {
		a.log.Info("ignored an ActivityPub Create that is not a reply", "actor", actor.ID)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if note.AttributedTo != actor.ID || !activitypub.SameOrigin(note.ID, actor.ID) {
		http.Error(w, "that reply does not belong to its actor", http.StatusBadRequest)
		return
	}
	if _, err := a.posts.ByGUID(r.Context(), note.InReplyTo); err != nil {
		http.Error(w, "that reply names no post here", http.StatusBadRequest)
		return
	}

	name := actor.Name
	if name == "" {
		name = actor.PreferredUsername
	}
	source, err := a.sources.EnsureFederatedSource(r.Context(), actor.ID, name)
	if err != nil {
		a.log.Error("could not prepare a source for an ActivityPub reply", "actor", actor.ID, "error", err)
		http.Error(w, "could not store that reply", http.StatusInternalServerError)
		return
	}

	item := feed.Item{
		GUID:            note.ID,
		GUIDIsPermalink: true,
		Link:            note.URL,
		HTML:            note.Content,
		Title:           note.Name,
		Author:          name,
		InReplyTo:       note.InReplyTo,
		Published:       activityTime(note.Published),
		Updated:         activityTime(note.Updated),
		Source:          &feed.Source{URL: actor.ID, Name: name},
	}
	if item.Link == "" {
		item.Link = note.ID
	}
	if note.Source != nil && strings.EqualFold(note.Source.MediaType, "text/markdown") {
		item.Markdown = note.Source.Content
	}

	payload, err := json.Marshal(activity)
	if err != nil {
		http.Error(w, "could not store that reply", http.StatusInternalServerError)
		return
	}
	if err := a.sources.IngestItem(r.Context(), source, payload, &item); err != nil {
		a.log.Error("could not ingest an ActivityPub reply", "actor", actor.ID, "note", note.ID, "error", err)
		http.Error(w, "could not store that reply", http.StatusInternalServerError)
		return
	}

	a.log.Info("accepted an ActivityPub reply", "actor", actor.ID, "note", note.ID, "to", note.InReplyTo)
	w.WriteHeader(http.StatusAccepted)
}

func activityTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func (a *App) authenticateDelivery(r *http.Request, body []byte) (*activitypub.Activity, *activitypub.Actor, error) {
	var activity activitypub.Activity
	if err := json.Unmarshal(body, &activity); err != nil {
		return nil, nil, err
	}
	if activity.Type == "" || activity.Actor == "" {
		return nil, nil, errors.New("web: the delivery names no actor or type")
	}

	signature, err := activitypub.ReadSignature(r)
	if err != nil {
		return nil, nil, err
	}
	if !activitypub.SameOrigin(signature.KeyID, activity.Actor) {
		return nil, nil, activitypub.ErrOriginMismatch
	}
	if activity.ID != "" && !activitypub.SameOrigin(activity.ID, activity.Actor) {
		return nil, nil, activitypub.ErrOriginMismatch
	}

	if blocked, why := a.deliveryBlocked(r, &activity); blocked {
		return nil, nil, errors.New("web: " + why)
	}

	actor, err := a.remoteActor(r.Context(), 0, activity.Actor)
	if err != nil {
		return nil, nil, err
	}
	if actor.PublicKey.ID != signature.KeyID {
		return nil, nil, activitypub.ErrOriginMismatch
	}

	key, err := activitypub.ParsePublicKey(actor.PublicKey.PublicKeyPEM)
	if err != nil {
		return nil, nil, err
	}
	if err := signature.Verify(r, body, key); err != nil {
		return nil, nil, err
	}
	return &activity, actor, nil
}

func (a *App) deliveryBlocked(r *http.Request, activity *activitypub.Activity) (bool, string) {
	filter, err := a.moderation.FilterFor(r.Context(), 0)
	if err != nil {
		return false, ""
	}
	return filter.Hides(activity.ID, activity.Actor, "", "")
}

func (a *App) remoteActor(ctx context.Context, _ int64, uri string) (*activitypub.Actor, error) {
	if actor, ok := a.ap.CachedActor(ctx, uri); ok {
		return actor, nil
	}

	actor, err := a.apClient.FetchActor(ctx, uri, a.instanceIdentity(ctx))
	if err != nil {
		return nil, err
	}
	if err := a.ap.RememberActor(ctx, actor); err != nil {
		a.log.Error("could not cache a remote actor", "error", err)
	}
	return actor, nil
}

func (a *App) acceptFollow(w http.ResponseWriter, r *http.Request,
	accountID int64, handle string, activity *activitypub.Activity, actor *activitypub.Actor) {

	if activity.ObjectID() != a.actorURI(handle) {
		http.Error(w, "that follow names somebody else", http.StatusBadRequest)
		return
	}

	if err := a.ap.AddFollower(r.Context(), accountID, actor); err != nil {
		a.log.Error("could not record a follower", "error", err)
		http.Error(w, "could not record that follow", http.StatusInternalServerError)
		return
	}

	a.queueAccept(context.WithoutCancel(r.Context()), accountID, handle, activity, actor)
	w.WriteHeader(http.StatusAccepted)
}

func (a *App) acceptReaction(w http.ResponseWriter, r *http.Request,
	activity *activitypub.Activity, actor *activitypub.Actor) {

	target := activity.ObjectID()
	if target == "" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if _, err := a.posts.ByGUID(r.Context(), target); err != nil {
		a.log.Info("ignored a reaction to something that is not ours",
			"type", activity.Type, "object", target)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if err := a.ap.RecordReaction(r.Context(), target, actor.ID, activity.Type, activity.ID); err != nil {
		a.log.Error("could not record a reaction", "error", err)
	}
	w.WriteHeader(http.StatusAccepted)
}

func (a *App) followingCollection(w http.ResponseWriter, r *http.Request) {
	_, canonical, err := a.posts.AccountByHandle(r.Context(), r.PathValue("handle"))
	if err != nil || canonical == "" {
		http.Error(w, "no such account", http.StatusNotFound)
		return
	}

	writeActivityJSON(w, http.StatusOK, orderedCollection{
		Context:    "https://www.w3.org/ns/activitystreams",
		ID:         a.actorURI(canonical) + "/following",
		Type:       "OrderedCollection",
		TotalItems: 0,
		Items:      []any{},
	})
}

func (a *App) sharedInbox(w http.ResponseWriter, r *http.Request) {
	if !a.inboxLimit.allow(clientIP(r, a.behindProxy)) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, inboxMaxBytes))
	if err != nil {
		http.Error(w, "that delivery is too large", http.StatusRequestEntityTooLarge)
		return
	}

	activity, actor, err := a.authenticateDelivery(r, body)
	if err != nil {
		a.log.Warn("refused a shared inbox delivery",
			"error", err, "ip", clientIP(r, a.behindProxy))
		http.Error(w, "that delivery could not be authenticated", http.StatusUnauthorized)
		return
	}
	if a.ap.AlreadySeen(r.Context(), activity.ID) {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	accountID, handle := a.addressee(r, activity)
	a.dispatch(w, r, accountID, handle, activity, actor)
}

func (a *App) addressee(r *http.Request, activity *activitypub.Activity) (int64, string) {
	for _, uri := range append(append([]string{activity.ObjectID()}, activity.To...), activity.Cc...) {
		handle, ok := strings.CutPrefix(uri, a.posts.BaseURL()+"/users/")
		if !ok {
			continue
		}
		handle, _, _ = strings.Cut(handle, "/")
		if accountID, canonical, err := a.posts.AccountByHandle(r.Context(), handle); err == nil && accountID != 0 {
			return accountID, canonical
		}
	}

	if note, err := activity.Note(); err == nil && note.InReplyTo != "" {
		if post, err := a.posts.ByGUID(r.Context(), note.InReplyTo); err == nil {
			return post.AccountID, post.Handle
		}
	}
	return 0, ""
}

func (a *App) undo(w http.ResponseWriter, r *http.Request,
	accountID int64, activity *activitypub.Activity, actor *activitypub.Actor) {

	undone, err := activity.Undone()
	if err != nil {
		a.log.Info("ignored an Undo we could not read", "actor", actor.ID)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if undone.Actor != "" && undone.Actor != actor.ID {
		http.Error(w, "that undo names somebody else's activity", http.StatusBadRequest)
		return
	}

	switch undone.Type {
	case "Follow":
		if err := a.ap.RemoveFollower(r.Context(), accountID, actor.ID); err != nil {
			a.log.Error("could not remove a follower", "error", err)
		}
	case "Like", "Announce":
		if err := a.ap.ForgetReaction(r.Context(), undone.ObjectID(), actor.ID, undone.Type); err != nil {
			a.log.Error("could not forget a reaction", "error", err)
		}
	default:
		a.log.Info("ignored an Undo of an activity we do not track",
			"type", undone.Type, "actor", actor.ID)
	}
	w.WriteHeader(http.StatusAccepted)
}
