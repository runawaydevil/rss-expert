package feedout

import (
	"encoding/xml"
	"strconv"

	"github.com/runawaydevil/rss-social/internal/feed"
)

var wellKnownPrefixes = map[string]string{
	feed.NSSource:       "source",
	feed.NSThread:       "thr",
	feed.NSAtom:         "atom",
	feed.NSContent:      "content",
	feed.NSDublinCore:   "dc",
	feed.NSMediaRSS:     "media",
	feed.NSITunes:       "itunes",
	feed.NSSlash:        "slash",
	feed.NSSyndication:  "sy",
	feed.NSWellFormed:   "wfw",
	feed.NSSourceLegacy: "source",
}

type binding struct {
	prefix string
	uri    string
}

type namespaces struct {
	byURI     map[string]string
	order     []binding
	generated int
}

func newNamespaces() *namespaces {
	return &namespaces{byURI: make(map[string]string)}
}

func (n *namespaces) use(uri string) string {
	if uri == "" || uri == feed.NSXML {
		return ""
	}
	if p, ok := n.byURI[uri]; ok {
		return p
	}
	p, known := wellKnownPrefixes[uri]
	if !known || n.prefixTaken(p) {
		n.generated++
		p = "ns" + strconv.Itoa(n.generated)
	}
	n.byURI[uri] = p
	n.order = append(n.order, binding{p, uri})
	return p
}

func (n *namespaces) prefixTaken(prefix string) bool {
	for _, b := range n.order {
		if b.prefix == prefix {
			return true
		}
	}
	return false
}

func (n *namespaces) qualify(name xml.Name) string {
	if name.Space == "" {
		return name.Local
	}
	if name.Space == feed.NSXML {
		return "xml:" + name.Local
	}
	if p, ok := n.byURI[name.Space]; ok {
		return p + ":" + name.Local
	}
	return name.Local
}

func collectNamespaces(f *feed.Feed) *namespaces {
	n := newNamespaces()

	if f.Self != "" || len(f.Accounts) > 0 || usesSource(f) {
		n.use(feed.NSSource)
	}
	if usesThreading(f) {
		n.use(feed.NSThread)
	}
	if usesAtom(f) {
		n.use(feed.NSAtom)
	}

	for i := range f.Unknown {
		registerTree(n, &f.Unknown[i])
	}
	for i := range f.Items {
		for j := range f.Items[i].Unknown {
			registerTree(n, &f.Items[i].Unknown[j])
		}
	}
	return n
}

func registerTree(n *namespaces, el *feed.Element) {
	n.use(el.Name.Space)
	for _, a := range el.Attrs {
		n.use(a.Name.Space)
	}
	for i := range el.Children {
		registerTree(n, &el.Children[i])
	}
}

func usesSource(f *feed.Feed) bool {
	for i := range f.Items {
		it := &f.Items[i]
		if it.Markdown != "" || it.InReplyTo != "" || it.Comments != nil {
			return true
		}
	}
	return false
}

func usesThreading(f *feed.Feed) bool {
	for i := range f.Items {
		if f.Items[i].InReplyTo != "" {
			return true
		}
	}
	return false
}

func usesAtom(f *feed.Feed) bool {
	for i := range f.Items {
		it := &f.Items[i]
		if !it.Updated.IsZero() && !it.Updated.Equal(it.Published) {
			return true
		}
	}
	return false
}
