package web

import (
	"fmt"
	"html/template"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/microcosm-cc/bluemonday"

	"github.com/runawaydevil/rss-expert/internal/feed"
	"github.com/runawaydevil/rss-expert/internal/ingest"
	"github.com/runawaydevil/rss-expert/internal/media"
	"github.com/runawaydevil/rss-expert/internal/publish"
)

type postView struct {
	Author       string
	Initial      string
	Site         string
	SiteURL      string
	URL          string
	Title        string
	HTML         template.HTML
	QuotedAuthor string
	QuotedText   string
	PublishedISO string
	Age          string
	Arrival      string
	ArrivalTip   string
	Read         bool
	Saved        bool
	Long         bool
	Visibility   string
	Key          string
	InReplyTo    string
	Local        bool
	ID           int64
	GUID         string
	ReplyCount   int
	Attachments  []attachmentView
	Elsewhere    []attachmentView
}

type attachmentView struct {
	URL     string
	Type    string
	Alt     string
	Kind    string
	Size    string
	Width   int
	Height  int
	Offsite bool
}

func (a attachmentView) IsImage() bool { return a.Kind == "image" }
func (a attachmentView) IsAudio() bool { return a.Kind == "audio" }
func (a attachmentView) IsVideo() bool { return a.Kind == "video" }

func attachments(files []*media.File) []attachmentView {
	out := make([]attachmentView, 0, len(files))
	for _, file := range files {
		out = append(out, attachmentView{
			URL:    file.URL(),
			Type:   file.MediaType,
			Alt:    file.Alt,
			Kind:   kindOf(file.MediaType),
			Size:   humanBytes(file.Bytes),
			Width:  file.Width,
			Height: file.Height,
		})
	}
	return out
}

func elsewhere(list []feed.Enclosure) []attachmentView {
	var out []attachmentView
	for _, e := range list {
		kind := kindOf(e.Type)
		if kind == "" || firstURL(e.URL) == "" {
			continue
		}
		out = append(out, attachmentView{
			URL:     e.URL,
			Type:    e.Type,
			Kind:    kind,
			Size:    humanBytes(e.Length),
			Offsite: true,
		})
	}
	return out
}

func kindOf(mediaType string) string {
	switch {
	case strings.HasPrefix(mediaType, "image/"):
		return "image"
	case strings.HasPrefix(mediaType, "audio/"):
		return "audio"
	case strings.HasPrefix(mediaType, "video/"):
		return "video"
	}
	return ""
}

func humanBytes(n int64) string {
	switch {
	case n <= 0:
		return ""
	case n < 1<<20:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

const (
	tipWebSub   = "Pushed by the source the moment it was published, not fetched on a schedule."
	tipRSSCloud = "Pushed by the source over rssCloud, the older of the two push protocols."
	tipPolled   = "Fetched on a schedule. The interval adapts to how often this source publishes."
)

func samplePosts() []postView {
	return []postView{
		{
			Author:       "Alice",
			Initial:      "A",
			Site:         "alice.example",
			SiteURL:      "https://alice.example/",
			URL:          "https://alice.example/p/1",
			Title:        "What a feed reader owes the person reading it",
			HTML:         "<p>A reader that hides where something came from is not saving you effort, it is deciding on your behalf that provenance does not matter. It does. The same post arrives by three different routes, and which version you are looking at is a fact about the world, not an implementation detail.</p>",
			PublishedISO: "2026-07-23T19:40:00Z",
			Age:          "4 min",
			Arrival:      "via websub",
			ArrivalTip:   tipWebSub,
		},
		{
			Author:       "Bob",
			Initial:      "B",
			Site:         "bob.example",
			SiteURL:      "https://bob.example/",
			URL:          "https://bob.example/p/7",
			HTML:         "<p>Spent the morning reading someone else's XML and came away convinced the namespace is the whole ballgame.</p>",
			PublishedISO: "2026-07-23T19:22:00Z",
			Age:          "22 min",
			Arrival:      "polled",
			ArrivalTip:   tipPolled,
		},
		{
			Author:       "Carol",
			Initial:      "C",
			Site:         "carol.example",
			SiteURL:      "https://carol.example/",
			URL:          "https://carol.example/p/12",
			QuotedAuthor: "Alice",
			QuotedText:   "A reader that hides where something came from is not saving you effort…",
			HTML:         "<p>Agreed, though I would go further: if the reader knows two sources disagreed, it should be able to say <em>which</em> it believed and on what rule. Otherwise \"we resolved it\" is a nicer word for guessing.</p>",
			PublishedISO: "2026-07-23T18:44:00Z",
			Age:          "1 h",
			Arrival:      "via rsscloud",
			ArrivalTip:   tipRSSCloud,
		},
		{
			Author:       "Dave Winer",
			Initial:      "D",
			Site:         "scripting.com",
			SiteURL:      "https://scripting.com/",
			URL:          "https://scripting.com/2026/07/23.html",
			HTML:         "<p>A post that has replies carries a comments feed, giving the address of an RSS feed of the replies. Every level the same shape, all the way down. No API needed.</p>",
			PublishedISO: "2026-07-23T14:56:00Z",
			Age:          "5 h",
			Arrival:      "polled",
			ArrivalTip:   tipPolled,
		},
	}
}

func timelineViews(items []ingest.Item) []postView {
	out := make([]postView, 0, len(items))
	for i := range items {
		item := &items[i]

		view := postView{
			Author:       item.DisplayAuthor(),
			Initial:      initial(item.DisplayAuthor()),
			Site:         item.Host(),
			SiteURL:      firstURL(item.SourceSiteURL, item.OriginURL, item.SourceFeedURL),
			URL:          firstURL(item.Link, item.Key),
			Title:        item.Title,
			HTML:         template.HTML(bluemonday.UGCPolicy().Sanitize(item.HTML)),
			PublishedISO: item.Published.Format(time.RFC3339),
			Age:          age(item.Published),
			Arrival:      "polled",
			ArrivalTip:   tipPolled,
		}
		view.Elsewhere = elsewhere(item.Enclosures)
		view.Long = view.URL != "" && isLong(item.HTML)
		if item.Edited() {
			view.Arrival = "edited"
			view.ArrivalTip = "The author changed this after publishing it. You are seeing the later version."
		}
		out = append(out, view)
	}
	return out
}

const longPostRunes = 900

func isLong(htmlContent string) bool {
	count := 0
	inTag := false
	for _, r := range htmlContent {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			count++
			if count > longPostRunes {
				return true
			}
		}
	}
	return false
}

func initial(name string) string {
	for _, r := range name {
		return strings.ToUpper(string(r))
	}
	return "?"
}

func firstURL(candidates ...string) string {
	for _, c := range candidates {
		if u, err := url.Parse(c); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			return c
		}
	}
	return ""
}

func age(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d d", int(d.Hours()/24))
	}
	return t.Format("2006-01-02")
}

func localView(post *publish.Post) postView {
	view := postView{
		Author:       post.Handle,
		Initial:      initial(post.Handle),
		Site:         post.Handle,
		SiteURL:      "/users/" + post.Handle,
		URL:          "/p/" + strconv.FormatInt(post.ID, 10),
		Title:        post.Title,
		HTML:         template.HTML(post.HTML),
		PublishedISO: post.Published.Format(time.RFC3339),
		Age:          age(post.Published),
		Arrival:      "written here",
		ArrivalTip:   "Published on this instance. It travels out in this account's own feed.",
		Local:        true,
		ID:           post.ID,
		GUID:         post.GUID,
		InReplyTo:    post.InReplyTo,
		ReplyCount:   post.ReplyCount,
		Attachments:  attachments(post.Media),
	}
	if !post.Public() {
		view.Visibility = post.Visibility
	}
	view.Long = isLong(post.HTML)
	if post.Edited() {
		view.Arrival = "edited"
		view.ArrivalTip = "The author changed this after publishing it. The edit travels in the same feed item."
	}
	return view
}

func localViews(posts []*publish.Post) []postView {
	out := make([]postView, 0, len(posts))
	for _, post := range posts {
		out = append(out, localView(post))
	}
	return out
}
