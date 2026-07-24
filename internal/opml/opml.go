package opml

import (
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrNotOPML = errors.New("opml: that document is not an outline")

type Subscription struct {
	Title    string
	FeedURL  string
	SiteURL  string
	Category string
}

type document struct {
	XMLName xml.Name `xml:"opml"`
	Version string   `xml:"version,attr"`
	Head    head     `xml:"head"`
	Body    body     `xml:"body"`
}

type head struct {
	Title       string `xml:"title,omitempty"`
	DateCreated string `xml:"dateCreated,omitempty"`
	OwnerName   string `xml:"ownerName,omitempty"`
}

type body struct {
	Outlines []outline `xml:"outline"`
}

type outline struct {
	Text     string    `xml:"text,attr,omitempty"`
	Title    string    `xml:"title,attr,omitempty"`
	Type     string    `xml:"type,attr,omitempty"`
	XMLURL   string    `xml:"xmlUrl,attr,omitempty"`
	HTMLURL  string    `xml:"htmlUrl,attr,omitempty"`
	Outlines []outline `xml:"outline"`
}

func Parse(data []byte) ([]Subscription, error) {
	var doc document
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotOPML, err)
	}
	if len(doc.Body.Outlines) == 0 {
		return nil, ErrNotOPML
	}

	var out []Subscription
	var walk func(items []outline, category string)
	walk = func(items []outline, category string) {
		for _, item := range items {
			label := firstNonEmpty(item.Title, item.Text)

			if strings.TrimSpace(item.XMLURL) != "" {
				out = append(out, Subscription{
					Title:    label,
					FeedURL:  strings.TrimSpace(item.XMLURL),
					SiteURL:  strings.TrimSpace(item.HTMLURL),
					Category: category,
				})
				continue
			}

			nested := category
			if label != "" {
				if nested == "" {
					nested = label
				} else {
					nested = nested + "/" + label
				}
			}
			walk(item.Outlines, nested)
		}
	}
	walk(doc.Body.Outlines, "")

	if len(out) == 0 {
		return nil, ErrNotOPML
	}
	return out, nil
}

func Render(title, owner string, subscriptions []Subscription, now time.Time) ([]byte, error) {
	grouped := map[string][]Subscription{}
	var order []string
	for _, sub := range subscriptions {
		if _, seen := grouped[sub.Category]; !seen {
			order = append(order, sub.Category)
		}
		grouped[sub.Category] = append(grouped[sub.Category], sub)
	}

	doc := document{
		Version: "2.0",
		Head: head{
			Title:       title,
			DateCreated: now.UTC().Format(time.RFC1123Z),
			OwnerName:   owner,
		},
	}

	for _, category := range order {
		items := make([]outline, 0, len(grouped[category]))
		for _, sub := range grouped[category] {
			items = append(items, outline{
				Text:    firstNonEmpty(sub.Title, sub.FeedURL),
				Title:   firstNonEmpty(sub.Title, sub.FeedURL),
				Type:    "rss",
				XMLURL:  sub.FeedURL,
				HTMLURL: sub.SiteURL,
			})
		}
		if category == "" {
			doc.Body.Outlines = append(doc.Body.Outlines, items...)
			continue
		}
		doc.Body.Outlines = append(doc.Body.Outlines, outline{
			Text: category, Title: category, Outlines: items,
		})
	}

	body, err := xml.MarshalIndent(doc, "", "\t")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(body, '\n')...), nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
