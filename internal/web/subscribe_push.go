package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/runawaydevil/rss-expert/internal/ingest"
	"github.com/runawaydevil/rss-expert/internal/push"
)

func (a *App) AskForPush(ctx context.Context, source *ingest.Source) {
	result, err := a.fetcher.Get(ctx, source.FeedURL, nil)
	if err != nil {
		return
	}

	found := push.Find(result.URL, result.Header, result.Body)
	if found.Self == "" {
		found.Self = source.FeedURL
	}
	if err := a.push.Remember(ctx, source.ID, found); err != nil {
		a.log.Error("could not record push endpoints", "source", source.ID, "error", err)
		return
	}

	if found.Hub != "" {
		a.talkToHub(ctx, source.ID, found.Hub, found.Self, "subscribe")
	}
	if found.Cloud != nil {
		a.RegisterWithCloud(ctx, source.ID, found.Cloud.Endpoint(), found.Self)
	}
}

func (a *App) talkToHub(ctx context.Context, sourceID int64, hub, topic, mode string) {
	secret := ""
	if mode == "subscribe" {
		fresh, err := push.NewSecret()
		if err != nil {
			return
		}
		secret = fresh
	}

	intent := push.Subscription{
		SourceID: sourceID,
		Protocol: push.WebSub,
		Topic:    topic,
		Mode:     mode,
		Secret:   secret,
	}
	if err := a.push.Intend(ctx, intent); err != nil {
		a.log.Error("could not record a push intent", "source", sourceID, "error", err)
		return
	}

	callback := a.posts.BaseURL() + "/websub/" + strconv.FormatInt(sourceID, 10)
	form := push.SubscribeForm(callback, topic, secret, mode)

	header := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}
	result, err := a.fetcher.Post(ctx, hub, header, []byte(form.Encode()))
	if err != nil {
		a.log.Info("a hub could not be reached", "hub", hub, "error", err)
		return
	}
	if result.StatusCode >= 300 {
		a.log.Info("a hub refused the subscription", "hub", hub, "status", result.StatusCode)
		return
	}
	a.log.Info("asked a hub for push", "hub", hub, "topic", topic, "mode", mode)
}

func (a *App) RegisterWithCloud(ctx context.Context, sourceID int64, endpoint, topic string) {
	intent := push.Subscription{
		SourceID: sourceID,
		Protocol: push.RSSCloud,
		Topic:    topic,
		Mode:     "subscribe",
	}
	if err := a.push.Intend(ctx, intent); err != nil {
		a.log.Error("could not record a cloud intent", "source", sourceID, "error", err)
		return
	}

	base, err := url.Parse(a.posts.BaseURL())
	if err != nil {
		return
	}
	port := 443
	if base.Scheme == "http" {
		port = 80
	}
	if explicit := base.Port(); explicit != "" {
		port, _ = strconv.Atoi(explicit)
	}

	form := push.CloudForm("/rsscloud/"+strconv.FormatInt(sourceID, 10), base.Hostname(), port, topic)
	header := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}

	result, err := a.fetcher.Post(ctx, endpoint, header, []byte(form.Encode()))
	if err != nil {
		a.log.Info("a cloud could not be reached", "cloud", endpoint, "error", err)
		return
	}
	if result.StatusCode >= 300 || strings.Contains(string(result.Body), `success="false"`) {
		a.log.Info("a cloud refused the registration", "cloud", endpoint, "status", result.StatusCode)
		return
	}
	a.log.Info("registered with a cloud", "cloud", endpoint, "topic", topic)
}

func (a *App) RenewPush(ctx context.Context) int {
	due, err := a.push.DueForRenewal(ctx, time.Now().UTC(), 50)
	if err != nil {
		a.log.Error("could not list subscriptions to renew", "error", err)
		return 0
	}

	for _, item := range due {
		switch item.Protocol {
		case push.WebSub:
			a.talkToHub(ctx, item.SourceID, item.Hub, item.Topic, "subscribe")
		case push.RSSCloud:
			a.RegisterWithCloud(ctx, item.SourceID, item.Cloud, item.Topic)
		}
	}
	return len(due)
}

func (a *App) LeaveHub(ctx context.Context, source *ingest.Source) {
	var hub, topic string
	err := a.db.Read.QueryRowContext(ctx,
		`select coalesce(hub_url, ''), coalesce(self_link, feed_url) from source where id = ?`,
		source.ID).Scan(&hub, &topic)
	if err != nil || hub == "" {
		return
	}
	a.talkToHub(ctx, source.ID, hub, topic, "unsubscribe")
}
