package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/runawaydevil/rss-expert/internal/feedin"
	"github.com/runawaydevil/rss-expert/internal/ledger"
	"github.com/runawaydevil/rss-expert/internal/push"
)

const pushBodyLimit = 8 << 20

func (a *App) websubCallback(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("source"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if r.Method == http.MethodGet {
		a.verifyIntent(w, r, id)
		return
	}
	a.acceptPush(w, r, id)
}

func (a *App) verifyIntent(w http.ResponseWriter, r *http.Request, sourceID int64) {
	query := r.URL.Query()
	mode := query.Get("hub.mode")
	topic := query.Get("hub.topic")

	intent, err := a.push.Intent(r.Context(), sourceID, push.WebSub)
	if err != nil || intent.Mode != mode || intent.Topic != topic {
		a.log.Info("refused a hub verification nobody asked for",
			"source", sourceID, "mode", mode, "topic", topic)
		http.NotFound(w, r)
		return
	}

	lease := push.LeaseAsked
	if seconds, err := strconv.Atoi(query.Get("hub.lease_seconds")); err == nil && seconds > 0 {
		lease = time.Duration(seconds) * time.Second
	}
	if err := a.push.Confirm(r.Context(), sourceID, push.WebSub, lease); err != nil {
		a.log.Error("could not record a confirmed subscription", "source", sourceID, "error", err)
	}

	a.log.Info("hub subscription confirmed", "source", sourceID, "mode", mode, "lease", lease)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, query.Get("hub.challenge"))
}

func (a *App) acceptPush(w http.ResponseWriter, r *http.Request, sourceID int64) {
	ctx := r.Context()

	source, err := a.sources.SourceByID(ctx, sourceID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, pushBodyLimit))
	if err != nil {
		http.Error(w, "that delivery could not be read", http.StatusBadRequest)
		return
	}

	secret, err := a.push.SecretFor(ctx, sourceID)
	if err != nil {
		a.log.Error("could not read the secret for a source", "source", sourceID, "error", err)
		http.Error(w, "not now", http.StatusInternalServerError)
		return
	}

	if err := push.CheckSignature(r.Header.Get("X-Hub-Signature"), secret, body); err != nil {
		a.log.Warn("a push delivery was refused", "source", sourceID, "feed", source.FeedURL, "reason", err)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	parsed, err := feedin.Parse(body)
	if err != nil {
		a.log.Info("a push delivery did not parse", "source", sourceID, "error", err)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	result, err := a.sources.Ingest(ctx, source, body, r.Header.Get("Content-Type"), parsed)
	if err != nil {
		a.log.Error("a push delivery could not be stored", "source", sourceID, "error", err)
		http.Error(w, "not now", http.StatusInternalServerError)
		return
	}

	a.sources.MarkPushed(ctx, sourceID, time.Now().UTC())
	a.log.Info("arrived by push", "source", sourceID, "feed", source.FeedURL,
		"observations", result.Observations, "converged", result.Converged)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) cloudCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	id, err := strconv.ParseInt(r.PathValue("source"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if challenge := r.URL.Query().Get("challenge"); challenge != "" {
		intent, err := a.push.Intent(ctx, id, push.RSSCloud)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := a.push.Confirm(ctx, id, push.RSSCloud, push.CloudLease); err != nil {
			a.log.Error("could not record a cloud registration", "source", id, "error", err)
		}

		a.log.Info("cloud registration confirmed", "source", id, "topic", intent.Topic)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, challenge)
		return
	}

	source, err := a.sources.SourceByID(ctx, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	a.log.Info("a cloud says this feed changed", "source", id, "feed", source.FeedURL)
	if err := a.poller.Fetch(ctx, source); err != nil {
		a.log.Info("the feed a cloud pinged us about could not be read", "source", id, "error", err)
	} else {
		a.sources.MarkPushed(ctx, id, time.Now().UTC())
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) hubEndpoint(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if err := r.ParseForm(); err != nil {
		http.Error(w, "hub.mode, hub.topic and hub.callback are required", http.StatusBadRequest)
		return
	}

	mode := r.PostFormValue("hub.mode")
	topic := r.PostFormValue("hub.topic")

	if mode == "publish" {
		a.publishToHub(w, r, r.PostFormValue("hub.url"))
		return
	}
	if mode != "subscribe" && mode != "unsubscribe" {
		http.Error(w, "hub.mode must be subscribe, unsubscribe or publish", http.StatusBadRequest)
		return
	}

	callback := r.PostFormValue("hub.callback")
	if !a.weServe(topic) {
		http.Error(w, "this hub only carries topics from this instance", http.StatusNotFound)
		return
	}
	if !plausibleCallback(callback) {
		http.Error(w, "hub.callback must be an http or https address", http.StatusBadRequest)
		return
	}

	challenge, err := push.NewSecret()
	if err != nil {
		http.Error(w, "not now", http.StatusInternalServerError)
		return
	}

	id, err := a.push.Pending(ctx, topic, callback, r.PostFormValue("hub.secret"), mode, challenge)
	if err != nil {
		a.log.Error("could not record a subscriber", "topic", topic, "error", err)
		http.Error(w, "not now", http.StatusInternalServerError)
		return
	}

	a.log.Info("a subscriber asked to be told", "topic", topic, "callback", callback, "mode", mode)
	go a.verifySubscriber(context.WithoutCancel(ctx), id)

	w.WriteHeader(http.StatusAccepted)
}

func (a *App) verifySubscriber(ctx context.Context, id int64) {
	sub, err := a.push.Subscriber(ctx, id)
	if err != nil {
		return
	}

	target, err := url.Parse(sub.Callback)
	if err != nil {
		return
	}

	query := target.Query()
	query.Set("hub.mode", sub.Mode)
	query.Set("hub.topic", sub.Topic)
	query.Set("hub.challenge", sub.Challenge)
	query.Set("hub.lease_seconds", strconv.Itoa(int(push.HubLease.Seconds())))
	target.RawQuery = query.Encode()

	result, err := a.fetcher.Get(ctx, target.String(), nil)
	if err != nil || result.StatusCode < 200 || result.StatusCode >= 300 {
		a.log.Info("a subscriber did not confirm", "callback", sub.Callback, "error", err)
		a.push.DropSubscriber(ctx, id)
		return
	}
	if strings.TrimSpace(string(result.Body)) != sub.Challenge {
		a.log.Info("a subscriber echoed the wrong challenge", "callback", sub.Callback)
		a.push.DropSubscriber(ctx, id)
		return
	}

	if sub.Mode == "unsubscribe" {
		a.push.DropSubscriber(ctx, id)
		a.log.Info("subscriber left", "topic", sub.Topic, "callback", sub.Callback)
		return
	}
	if err := a.push.Verified(ctx, id); err != nil {
		a.log.Error("could not mark a subscriber verified", "id", id, "error", err)
		return
	}
	a.log.Info("subscriber confirmed", "topic", sub.Topic, "callback", sub.Callback)
}

func (a *App) publishToHub(w http.ResponseWriter, r *http.Request, topic string) {
	if !a.weServe(topic) {
		http.Error(w, "this hub only carries topics from this instance", http.StatusNotFound)
		return
	}

	go a.Distribute(context.WithoutCancel(r.Context()), topic)
	w.WriteHeader(http.StatusAccepted)
}

func (a *App) Distribute(ctx context.Context, topic string) {
	body, mediaType, err := a.topicBody(ctx, topic)
	if err != nil {
		a.log.Error("could not build a topic to distribute", "topic", topic, "error", err)
		return
	}

	now := time.Now().UTC()

	subscribers, err := a.push.Subscribers(ctx, topic, now)
	if err != nil {
		a.log.Error("could not list subscribers", "topic", topic, "error", err)
		return
	}
	for _, sub := range subscribers {
		a.deliverPush(ctx, sub, topic, body, mediaType)
	}

	callbacks, err := a.push.CloudSubscribers(ctx, topic, now)
	if err != nil {
		a.log.Error("could not list cloud subscribers", "topic", topic, "error", err)
		return
	}
	for _, callback := range callbacks {
		a.pingCloud(ctx, callback, topic)
	}
}

func (a *App) deliverPush(ctx context.Context, sub push.Subscriber, topic string, body []byte, mediaType string) {
	header := http.Header{
		"Content-Type": {mediaType},
		"Link":         {`<` + a.hubURL() + `>; rel="hub", <` + topic + `>; rel="self"`},
	}
	if sub.Secret != "" {
		header.Set("X-Hub-Signature", push.Sign(sub.Secret, body))
	}

	attempt := ledger.Attempt{
		ItemKey:  topic,
		Target:   sub.Callback,
		Protocol: ledger.WebSub,
		At:       time.Now().UTC(),
	}

	result, err := a.fetcher.Post(ctx, sub.Callback, header, body)
	switch {
	case err != nil:
		attempt.Outcome = ledger.Failed
		attempt.ErrorDetail = err.Error()
	case result.StatusCode >= 400:
		attempt.Outcome = ledger.Failed
		attempt.HTTPStatus = result.StatusCode
	default:
		attempt.Outcome = ledger.OK
		attempt.HTTPStatus = result.StatusCode
	}

	if _, err := a.ledger.Record(ctx, attempt); err != nil {
		a.log.Error("could not record a push delivery", "target", sub.Callback, "error", err)
	}
	if attempt.Outcome == ledger.Failed && result == nil {
		a.log.Info("a subscriber could not be reached", "callback", sub.Callback, "error", err)
	}
}

func (a *App) pingCloud(ctx context.Context, callback, topic string) {
	form := url.Values{"url": {topic}}
	header := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}

	attempt := ledger.Attempt{
		ItemKey:  topic,
		Target:   callback,
		Protocol: ledger.RSSCloud,
		At:       time.Now().UTC(),
	}

	result, err := a.fetcher.Post(ctx, callback, header, []byte(form.Encode()))
	switch {
	case err != nil:
		attempt.Outcome = ledger.Failed
		attempt.ErrorDetail = err.Error()
	case result.StatusCode >= 400:
		attempt.Outcome = ledger.Failed
		attempt.HTTPStatus = result.StatusCode
	default:
		attempt.Outcome = ledger.OK
		attempt.HTTPStatus = result.StatusCode
	}

	if _, err := a.ledger.Record(ctx, attempt); err != nil {
		a.log.Error("could not record a cloud ping", "target", callback, "error", err)
	}
}

func (a *App) cloudRegister(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if err := r.ParseForm(); err != nil {
		cloudResult(w, false, "that registration could not be read")
		return
	}
	if !strings.EqualFold(r.PostFormValue("protocol"), "http-post") {
		cloudResult(w, false, "this cloud only speaks http-post")
		return
	}

	host := r.PostFormValue("domain")
	if host == "" {
		host, _, _ = strings.Cut(r.RemoteAddr, ":")
	}
	port := r.PostFormValue("port")
	path := r.PostFormValue("path")
	if host == "" || path == "" {
		cloudResult(w, false, "domain and path are required")
		return
	}

	scheme := "http"
	if port == "443" {
		scheme = "https"
	}
	if port != "" && port != "80" && port != "443" {
		host += ":" + port
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	callback := scheme + "://" + host + path

	var registered int
	for name, values := range r.PostForm {
		if !strings.HasPrefix(name, "url") {
			continue
		}
		for _, topic := range values {
			if !a.weServe(topic) {
				continue
			}
			if !a.cloudChallenge(ctx, callback, topic) {
				cloudResult(w, false, "the notification address did not answer our challenge")
				return
			}
			if err := a.push.RegisterCloud(ctx, topic, callback); err != nil {
				a.log.Error("could not register a cloud subscriber", "callback", callback, "error", err)
				cloudResult(w, false, "it could not be recorded")
				return
			}
			registered++
		}
	}

	if registered == 0 {
		cloudResult(w, false, "none of those feeds are published here")
		return
	}

	a.log.Info("cloud subscriber registered", "callback", callback, "feeds", registered)
	cloudResult(w, true, "subscription registered")
}

func (a *App) cloudChallenge(ctx context.Context, callback, topic string) bool {
	challenge, err := push.NewSecret()
	if err != nil {
		return false
	}

	target, err := url.Parse(callback)
	if err != nil {
		return false
	}
	query := target.Query()
	query.Set("url", topic)
	query.Set("challenge", challenge)
	target.RawQuery = query.Encode()

	result, err := a.fetcher.Get(ctx, target.String(), nil)
	if err != nil || result.StatusCode < 200 || result.StatusCode >= 300 {
		return false
	}
	return strings.TrimSpace(string(result.Body)) == challenge
}

func cloudResult(w http.ResponseWriter, ok bool, message string) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
	}
	io.WriteString(w, `<?xml version="1.0"?>`+"\n"+
		`<notifyResult success="`+strconv.FormatBool(ok)+`" msg="`+escapeAttr(message)+`"/>`+"\n")
}

func escapeAttr(s string) string {
	return strings.NewReplacer(`&`, "&amp;", `"`, "&quot;", `<`, "&lt;", `>`, "&gt;").Replace(s)
}

func plausibleCallback(raw string) bool {
	if raw == "" || len(raw) > push.MaxCallback {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func (a *App) weServe(topic string) bool {
	return topic != "" && strings.HasPrefix(topic, a.posts.BaseURL()+"/")
}

func (a *App) hubURL() string {
	return a.posts.BaseURL() + "/websub/hub"
}

func (a *App) topicBody(ctx context.Context, topic string) ([]byte, string, error) {
	base := a.posts.BaseURL()

	switch {
	case topic == base+"/users/rss.xml":
		body, err := a.posts.Firehose(ctx)
		return body, "application/rss+xml; charset=utf-8", err
	case strings.HasPrefix(topic, base+"/p/") && strings.HasSuffix(topic, "/replies.xml"):
		raw := strings.TrimSuffix(strings.TrimPrefix(topic, base+"/p/"), "/replies.xml")
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, "", errors.New("web: that topic is not published here")
		}
		body, err := a.posts.RepliesFeed(ctx, id)
		return body, "application/rss+xml; charset=utf-8", err
	case strings.HasPrefix(topic, base+"/users/") && strings.HasSuffix(topic, "/rss.xml"):
		handle := strings.TrimSuffix(strings.TrimPrefix(topic, base+"/users/"), "/rss.xml")
		body, err := a.posts.AccountFeed(ctx, handle)
		return body, "application/rss+xml; charset=utf-8", err
	}
	return nil, "", errors.New("web: that topic is not published here")
}
