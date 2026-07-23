package feed

import (
	"encoding/xml"
	"time"
)

const (
	NSSource       = "https://source.scripting.com/"
	NSSourceLegacy = "http://source.scripting.com/"

	NSThread      = "http://purl.org/syndication/thread/1.0"
	NSAtom        = "http://www.w3.org/2005/Atom"
	NSContent     = "http://purl.org/rss/1.0/modules/content/"
	NSDublinCore  = "http://purl.org/dc/elements/1.1/"
	NSMediaRSS    = "http://search.yahoo.com/mrss/"
	NSITunes      = "http://www.itunes.com/dtds/podcast-1.0.dtd"
	NSXMLNS       = "http://www.w3.org/2000/xmlns/"
	NSXML         = "http://www.w3.org/XML/1998/namespace"
	NSWellFormed  = "http://wellformedweb.org/CommentAPI/"
	NSSlash       = "http://purl.org/rss/1.0/modules/slash/"
	NSSyndication = "http://purl.org/rss/1.0/modules/syndication/"
)

type Feed struct {
	Title       string
	Link        string
	FeedLink    string
	Description string
	Language    string
	Updated     time.Time
	Self        string
	Accounts    []Account
	Items       []Item
	Unknown     []Element
}

type Account struct {
	Service string
	Handle  string
}

type Item struct {
	GUID            string
	GUIDIsPermalink bool
	Link            string
	Title           string
	HTML            string
	Markdown        string
	Author          string
	Published       time.Time
	Updated         time.Time
	InReplyTo       string
	CommentsPage    string
	Comments        *Comments
	Source          *Source
	Enclosures      []Enclosure
	Unknown         []Element
}

type Source struct {
	URL  string
	Name string
}

type Comments struct {
	Count   int
	FeedURL string
}

type Enclosure struct {
	URL    string
	Type   string
	Length int64
}

type Element struct {
	Name     xml.Name
	Attrs    []xml.Attr
	Text     string
	Children []Element
}

func (i *Item) Key() string {
	if i.GUID != "" {
		return i.GUID
	}
	return i.Link
}

func (i *Item) IsReply() bool { return i.InReplyTo != "" }

func (i *Item) HasMarkdown() bool { return i.Markdown != "" }

func (f *Feed) IsTextcast() bool {
	for i := range f.Items {
		if f.Items[i].HasMarkdown() {
			return true
		}
	}
	return false
}
