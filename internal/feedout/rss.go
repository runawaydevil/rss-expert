package feedout

import (
	"encoding/xml"
	"strconv"
	"time"

	"github.com/runawaydevil/rss-social/internal/feed"
)

type Cloud struct {
	Domain            string
	Port              int
	Path              string
	RegisterProcedure string
	Protocol          string
}

type RSSOptions struct {
	Generator string
	Docs      string
	Cloud     *Cloud
	BuildTime time.Time
}

func RSS(f *feed.Feed, o RSSOptions) []byte {
	ns := collectNamespaces(f)
	w := &writer{}
	w.buf.WriteString(xml.Header)

	root := []attr{{"version", "2.0"}}
	for _, b := range ns.order {
		root = append(root, attr{"xmlns:" + b.prefix, b.uri})
	}
	w.open("rss", root...)
	writeChannel(w, ns, f, o)
	w.close("rss")

	return w.buf.Bytes()
}

func writeChannel(w *writer, ns *namespaces, f *feed.Feed, o RSSOptions) {
	w.open("channel")
	w.optional("title", f.Title)
	w.optional("link", f.Link)
	w.optional("description", f.Description)
	w.optional("language", f.Language)
	w.optional("pubDate", rfc822(f.Updated))
	w.optional("lastBuildDate", rfc822(o.BuildTime))
	w.optional("generator", o.Generator)
	w.optional("docs", o.Docs)

	if c := o.Cloud; c != nil {
		w.leaf("cloud", "",
			attr{"domain", c.Domain},
			attr{"port", strconv.Itoa(c.Port)},
			attr{"path", c.Path},
			attr{"registerProcedure", c.RegisterProcedure},
			attr{"protocol", c.Protocol},
		)
	}

	if p := ns.byURI[feed.NSSource]; p != "" {
		w.optional(p+":self", f.Self)
		for _, a := range f.Accounts {
			if a.Handle == "" {
				continue
			}
			var list []attr
			if a.Service != "" {
				list = append(list, attr{"service", a.Service})
			}
			w.leaf(p+":account", a.Handle, list...)
		}
	}

	for i := range f.Unknown {
		w.element(ns, &f.Unknown[i])
	}
	for i := range f.Items {
		writeItem(w, ns, &f.Items[i])
	}
	w.close("channel")
}

func writeItem(w *writer, ns *namespaces, it *feed.Item) {
	w.open("item")
	w.optional("title", it.Title)
	w.optional("description", it.HTML)
	w.optional("pubDate", rfc822(it.Published))

	if it.GUID != "" {
		var list []attr
		if !it.GUIDIsPermalink {
			list = append(list, attr{"isPermaLink", "false"})
		}
		w.leaf("guid", it.GUID, list...)
	}
	if it.Link != "" && it.Link != it.GUID {
		w.leaf("link", it.Link)
	}
	if s := it.Source; s != nil && (s.URL != "" || s.Name != "") {
		var list []attr
		if s.URL != "" {
			list = append(list, attr{"url", s.URL})
		}
		w.leaf("source", s.Name, list...)
	}
	w.optional("author", it.Author)
	w.optional("comments", it.CommentsPage)

	for _, e := range it.Enclosures {
		w.leaf("enclosure", "",
			attr{"url", e.URL},
			attr{"length", strconv.FormatInt(e.Length, 10)},
			attr{"type", e.Type},
		)
	}

	source := ns.byURI[feed.NSSource]
	if source != "" {
		w.optional(source+":markdown", it.Markdown)
		w.optional(source+":inReplyTo", it.InReplyTo)
	}
	if thr := ns.byURI[feed.NSThread]; thr != "" && it.InReplyTo != "" {
		w.leaf(thr+":in-reply-to", "", attr{"ref", it.InReplyTo})
	}
	if source != "" && it.Comments != nil && it.Comments.FeedURL != "" {
		w.leaf(source+":comments", "",
			attr{"count", strconv.Itoa(it.Comments.Count)},
			attr{"feedUrl", it.Comments.FeedURL},
		)
	}
	if atom := ns.byURI[feed.NSAtom]; atom != "" && !it.Updated.IsZero() && !it.Updated.Equal(it.Published) {
		w.leaf(atom+":updated", it.Updated.UTC().Format(time.RFC3339))
	}

	for i := range it.Unknown {
		w.element(ns, &it.Unknown[i])
	}
	w.close("item")
}

func rfc822(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("Mon, 02 Jan 2006 15:04:05") + " GMT"
}
