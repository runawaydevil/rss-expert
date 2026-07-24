package feedin

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"

	"golang.org/x/net/html/charset"

	"github.com/runawaydevil/rss-expert/internal/feed"
)

var errNoRootElement = errors.New("feedin: document has no root element")

type consumedName struct {
	space string
	local string
}

var consumed = map[consumedName]bool{
	{feed.NSSource, "markdown"}:    true,
	{feed.NSSource, "inReplyTo"}:   true,
	{feed.NSSource, "comments"}:    true,
	{feed.NSSource, "account"}:     true,
	{feed.NSSource, "self"}:        true,
	{feed.NSThread, "in-reply-to"}: true,
	{feed.NSAtom, "updated"}:       true,
}

type channelScan struct {
	self     string
	accounts []feed.Account
	unknown  []feed.Element
	items    []itemScan
}

type itemScan struct {
	key           string
	guidPermalink bool
	markdown      string
	inReplyTo     string
	commentsPage  string
	comments      *feed.Comments
	source        *feed.Source
	unknown       []feed.Element
}

func scanXML(data []byte) (*channelScan, error) {
	root, err := decodeDocument(data)
	if err != nil {
		return nil, err
	}

	container := root
	if c := root.Child("", "channel"); c != nil {
		container = c
	}

	out := &channelScan{
		self:     container.ChildText(feed.NSSource, "self"),
		accounts: scanAccounts(container),
		unknown:  unknownChildren(container),
	}

	for _, el := range itemElements(root, container) {
		out.items = append(out.items, scanItem(el))
	}
	return out, nil
}

func itemElements(root, container *feed.Element) []*feed.Element {
	var out []*feed.Element
	for i := range container.Children {
		c := &container.Children[i]
		if c.Name.Local == "item" || c.Name.Local == "entry" {
			out = append(out, c)
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, local := range []string{"item", "entry"} {
		if found := root.Descendants(local); len(found) > 0 {
			return found
		}
	}
	return nil
}

func scanAccounts(container *feed.Element) []feed.Account {
	var out []feed.Account
	for i := range container.Children {
		c := &container.Children[i]
		if c.Name.Space != feed.NSSource || c.Name.Local != "account" {
			continue
		}
		handle := strings.TrimSpace(c.Text)
		if handle == "" {
			continue
		}
		out = append(out, feed.Account{Service: c.Attr("service"), Handle: handle})
	}
	return out
}

func scanItem(el *feed.Element) itemScan {
	it := itemScan{
		key:           itemKey(el),
		guidPermalink: true,
		markdown:      el.ChildText(feed.NSSource, "markdown"),
		inReplyTo:     el.ChildText(feed.NSSource, "inReplyTo"),
		commentsPage:  el.ChildText("", "comments"),
		unknown:       unknownChildren(el),
	}
	if g := el.Child("", "guid"); g != nil {
		if v := g.Attr("isPermaLink"); v != "" {
			it.guidPermalink = !strings.EqualFold(strings.TrimSpace(v), "false")
		}
	}
	if it.inReplyTo == "" {
		if thr := el.Child(feed.NSThread, "in-reply-to"); thr != nil {
			it.inReplyTo = firstNonEmpty(thr.Attr("ref"), thr.Attr("href"))
		}
	}
	if s := el.Child("", "source"); s != nil {
		url := s.Attr("url")
		name := strings.TrimSpace(s.Text)
		if url != "" || name != "" {
			it.source = &feed.Source{URL: url, Name: name}
		}
	}
	if c := el.Child(feed.NSSource, "comments"); c != nil {
		count, _ := strconv.Atoi(strings.TrimSpace(c.Attr("count")))
		it.comments = &feed.Comments{
			Count:   count,
			FeedURL: firstNonEmpty(c.Attr("feedUrl"), c.Attr("feedurl"), c.Text),
		}
	}
	return it
}

func itemKey(el *feed.Element) string {
	if g := el.Child("", "guid"); g != nil && g.Text != "" {
		return g.Text
	}
	if id := el.Child(feed.NSAtom, "id"); id != nil && id.Text != "" {
		return id.Text
	}
	if l := el.Child("", "link"); l != nil && l.Text != "" {
		return l.Text
	}
	for i := range el.Children {
		c := &el.Children[i]
		if c.Name.Local == "link" && c.Attr("href") != "" {
			rel := c.Attr("rel")
			if rel == "" || rel == "alternate" {
				return c.Attr("href")
			}
		}
	}
	return ""
}

func unknownChildren(el *feed.Element) []feed.Element {
	var out []feed.Element
	for i := range el.Children {
		c := &el.Children[i]
		if c.Name.Space == "" || c.Name.Local == "item" || c.Name.Local == "entry" {
			continue
		}
		if consumed[consumedName{c.Name.Space, c.Name.Local}] {
			continue
		}
		out = append(out, *c)
	}
	return out
}

func decodeDocument(data []byte) (*feed.Element, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	d.CharsetReader = charset.NewReaderLabel

	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return nil, errNoRootElement
		}
		if err != nil {
			return nil, err
		}
		if start, ok := tok.(xml.StartElement); ok {
			el, err := decodeElement(d, start)
			if err != nil {
				return nil, err
			}
			return &el, nil
		}
	}
}

func decodeElement(d *xml.Decoder, start xml.StartElement) (feed.Element, error) {
	el := feed.Element{Name: start.Name}
	el.Name.Space = canonicalSpace(el.Name.Space)
	for _, a := range start.Attr {
		if feed.IsNamespaceDeclaration(a) {
			continue
		}
		a.Name.Space = canonicalSpace(a.Name.Space)
		el.Attrs = append(el.Attrs, a)
	}

	var text strings.Builder
	for {
		tok, err := d.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				el.Text = strings.TrimSpace(text.String())
				return el, nil
			}
			return el, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			child, err := decodeElement(d, t)
			if err != nil {
				return el, err
			}
			el.Children = append(el.Children, child)
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			el.Text = strings.TrimSpace(text.String())
			return el, nil
		}
	}
}

func canonicalSpace(space string) string {
	if space == feed.NSSourceLegacy {
		return feed.NSSource
	}
	return space
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
