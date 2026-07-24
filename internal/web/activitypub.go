package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/runawaydevil/rss-expert/internal/activitypub"
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

func (a *App) actorDocument(r *http.Request, handle string) (*activitypub.Actor, error) {
	accountID, canonical, err := a.posts.AccountByHandle(r.Context(), handle)
	if err != nil || accountID == 0 {
		return nil, errNoSuchAccount
	}

	public, err := a.ap.PublicKeyFor(r.Context(), accountID)
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
		Discoverable:      true,
		PublicKey: &activitypub.PublicKey{
			ID:           a.keyID(canonical),
			Owner:        uri,
			PublicKeyPEM: public,
		},
	}

	if sites, err := a.sites.ForAccount(r.Context(), accountID); err == nil {
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
	actor, err := a.actorDocument(r, r.PathValue("handle"))
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

	posts, err := a.posts.ByHandle(r.Context(), canonical, 1)
	if err != nil {
		a.log.Error("could not read an account's posts", "error", err)
	}

	writeActivityJSON(w, http.StatusOK, orderedCollection{
		Context:    "https://www.w3.org/ns/activitystreams",
		ID:         a.actorURI(canonical) + "/outbox",
		Type:       "OrderedCollection",
		TotalItems: len(posts),
		Items:      []any{},
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

	switch activity.Type {
	case "Follow":
		a.acceptFollow(w, r, accountID, canonical, activity, actor)
	case "Undo":
		a.undoFollow(w, r, accountID, activity, actor)
	case "Delete":
		w.WriteHeader(http.StatusAccepted)
	default:
		a.log.Info("ignored an activity we do not handle yet",
			"type", activity.Type, "actor", actor.ID)
		w.WriteHeader(http.StatusAccepted)
	}
}

func (a *App) authenticateDelivery(r *http.Request, body []byte) (*activitypub.Activity, *activitypub.Actor, error) {
	var activity activitypub.Activity
	if err := json.Unmarshal(body, &activity); err != nil {
		return nil, nil, err
	}
	if activity.Type == "" || activity.Actor == "" {
		return nil, nil, errors.New("web: the delivery names no actor or type")
	}

	signature, err := activitypub.ParseSignature(r.Header.Get("Signature"))
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

	actor, err := a.resolveActor(r, activity.Actor)
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
	if err := activitypub.Verify(r, body, key, signature); err != nil {
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

func (a *App) resolveActor(r *http.Request, uri string) (*activitypub.Actor, error) {
	if actor, ok := a.ap.CachedActor(r.Context(), uri); ok {
		return actor, nil
	}

	actor, err := a.apClient.FetchActor(r.Context(), uri, a.instanceIdentity(r))
	if err != nil {
		return nil, err
	}
	if err := a.ap.RememberActor(r.Context(), actor); err != nil {
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

	a.queueAccept(r, accountID, handle, activity, actor)
	w.WriteHeader(http.StatusAccepted)
}

func (a *App) undoFollow(w http.ResponseWriter, r *http.Request,
	accountID int64, activity *activitypub.Activity, actor *activitypub.Actor) {

	if err := a.ap.RemoveFollower(r.Context(), accountID, actor.ID); err != nil {
		a.log.Error("could not remove a follower", "error", err)
	}
	w.WriteHeader(http.StatusAccepted)
}
