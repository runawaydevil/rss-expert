package indieweb

import (
	"bytes"
	"errors"
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var ErrNoDocument = errors.New("indieweb: nothing to read")

type Card struct {
	Name  string
	Photo string
	Note  string
	URLs  []string
}

type Page struct {
	Card          Card
	RelMe         []string
	Feeds         []Feed
	Authorization string
	TokenEndpoint string
	Micropub      string
	Webmention    string
}

type Feed struct {
	URL   string
	Title string
	Type  string
}

func Discover(base *url.URL, body []byte) (*Page, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, ErrNoDocument
	}

	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	page := &Page{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.A, atom.Link:
				readRel(page, base, n)
			}
			readCard(page, base, n)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)

	page.RelMe = dedupe(page.RelMe)
	page.Card.URLs = dedupe(page.Card.URLs)
	return page, nil
}

func readRel(page *Page, base *url.URL, n *html.Node) {
	rels := strings.Fields(strings.ToLower(attr(n, "rel")))
	if len(rels) == 0 {
		return
	}

	href := resolve(base, attr(n, "href"))
	if href == "" {
		return
	}

	for _, rel := range rels {
		switch rel {
		case "me":
			page.RelMe = append(page.RelMe, href)
		case "authorization_endpoint":
			page.Authorization = href
		case "token_endpoint":
			page.TokenEndpoint = href
		case "micropub":
			page.Micropub = href
		case "webmention":
			if page.Webmention == "" {
				page.Webmention = href
			}
		case "alternate":
			if kind := feedType(attr(n, "type")); kind != "" {
				page.Feeds = append(page.Feeds, Feed{
					URL: href, Title: attr(n, "title"), Type: kind,
				})
			}
		}
	}
}

func readCard(page *Page, base *url.URL, n *html.Node) {
	classes := strings.Fields(attr(n, "class"))

	inCard := false
	for _, class := range classes {
		if class == "h-card" {
			inCard = true
		}
	}
	if inCard {
		collectCard(page, base, n)
		return
	}
}

func collectCard(page *Page, base *url.URL, root *html.Node) {
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for _, class := range strings.Fields(attr(n, "class")) {
				switch class {
				case "p-name":
					if page.Card.Name == "" {
						page.Card.Name = text(n)
					}
				case "u-photo":
					if page.Card.Photo == "" {
						page.Card.Photo = resolve(base, firstAttr(n, "src", "href"))
					}
				case "p-note":
					if page.Card.Note == "" {
						page.Card.Note = text(n)
					}
				case "u-url":
					if href := resolve(base, firstAttr(n, "href", "src")); href != "" {
						page.Card.URLs = append(page.Card.URLs, href)
					}
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
}

func feedType(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(mediaType, ";")[0])) {
	case "application/rss+xml":
		return "rss"
	case "application/atom+xml":
		return "atom"
	case "application/feed+json", "application/json":
		return "json"
	}
	return ""
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, name) {
			return a.Val
		}
	}
	return ""
}

func firstAttr(n *html.Node, names ...string) string {
	for _, name := range names {
		if v := attr(n, name); v != "" {
			return v
		}
	}
	return ""
}

func text(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func resolve(base *url.URL, href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	parsed, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if base != nil {
		parsed = base.ResolveReference(parsed)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	parsed.Fragment = ""
	return parsed.String()
}

func dedupe(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := values[:0]
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func SameURL(a, b string) bool {
	left, err := url.Parse(a)
	if err != nil {
		return false
	}
	right, err := url.Parse(b)
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimPrefix(left.Hostname(), "www."),
		strings.TrimPrefix(right.Hostname(), "www.")) &&
		strings.TrimSuffix(left.Path, "/") == strings.TrimSuffix(right.Path, "/")
}
