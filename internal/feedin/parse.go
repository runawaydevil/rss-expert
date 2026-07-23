package feedin

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"

	"github.com/runawaydevil/rss-social/internal/feed"
)

var byteOrderMark = []byte{0xEF, 0xBB, 0xBF}

func Parse(data []byte) (*feed.Feed, error) {
	parsed, err := gofeed.NewParser().Parse(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("feedin: %w", err)
	}

	f := &feed.Feed{
		Title:       parsed.Title,
		Link:        parsed.Link,
		FeedLink:    parsed.FeedLink,
		Description: parsed.Description,
		Language:    parsed.Language,
		Updated:     derefTime(parsed.UpdatedParsed),
	}
	for _, gi := range parsed.Items {
		f.Items = append(f.Items, convertItem(gi))
	}

	if looksLikeXML(data) {
		if scan, err := scanXML(data); err == nil {
			overlay(f, scan)
		}
	}
	return f, nil
}

func convertItem(gi *gofeed.Item) feed.Item {
	it := feed.Item{
		GUID:            gi.GUID,
		GUIDIsPermalink: true,
		Link:            gi.Link,
		Title:           gi.Title,
		HTML:            firstNonEmpty(gi.Content, gi.Description),
		Author:          personName(gi),
		Published:       derefTime(gi.PublishedParsed),
		Updated:         derefTime(gi.UpdatedParsed),
	}
	if it.GUID == "" {
		it.GUID = it.Link
	}
	for _, e := range gi.Enclosures {
		if e == nil || e.URL == "" {
			continue
		}
		length, _ := strconv.ParseInt(strings.TrimSpace(e.Length), 10, 64)
		it.Enclosures = append(it.Enclosures, feed.Enclosure{
			URL:    e.URL,
			Type:   e.Type,
			Length: length,
		})
	}
	return it
}

func overlay(f *feed.Feed, scan *channelScan) {
	f.Self = scan.self
	f.Accounts = scan.accounts
	f.Unknown = scan.unknown

	byKey := make(map[string]*itemScan, len(scan.items))
	for i := range scan.items {
		k := scan.items[i].key
		if k == "" {
			continue
		}
		if _, seen := byKey[k]; !seen {
			byKey[k] = &scan.items[i]
		}
	}

	for i := range f.Items {
		it := &f.Items[i]
		s := byKey[it.GUID]
		if s == nil && it.Link != "" {
			s = byKey[it.Link]
		}
		if s == nil && len(scan.items) == len(f.Items) {
			s = &scan.items[i]
		}
		if s == nil {
			continue
		}
		it.GUIDIsPermalink = s.guidPermalink
		it.Markdown = s.markdown
		it.InReplyTo = s.inReplyTo
		it.CommentsPage = s.commentsPage
		it.Comments = s.comments
		it.Source = s.source
		it.Unknown = s.unknown
	}
}

func personName(gi *gofeed.Item) string {
	if len(gi.Authors) > 0 && gi.Authors[0] != nil {
		if n := gi.Authors[0].Name; n != "" {
			return n
		}
		return gi.Authors[0].Email
	}
	if gi.Author != nil {
		if gi.Author.Name != "" {
			return gi.Author.Name
		}
		return gi.Author.Email
	}
	return ""
}

func looksLikeXML(data []byte) bool {
	trimmed := bytes.TrimPrefix(data, byteOrderMark)
	trimmed = bytes.TrimLeft(trimmed, " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] == '<'
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}
