package web

import (
	"strings"
	"testing"

	"github.com/runawaydevil/rss-expert/internal/publish"
)

func TestIsLong(t *testing.T) {
	short := "<p>" + strings.Repeat("word ", 20) + "</p>"
	if isLong(short) {
		t.Fatal("a short post should not be marked long")
	}

	long := "<p>" + strings.Repeat("word ", 300) + "</p>"
	if !isLong(long) {
		t.Fatal("a post well past the threshold should be marked long")
	}

	markup := "<div" + strings.Repeat(` data-x="00000000"`, 100) + "></div><p>hi</p>"
	if isLong(markup) {
		t.Fatal("tag bytes must not count toward the length")
	}
}

func TestLocalViewMarksLongPosts(t *testing.T) {
	short := localView(&publish.Post{ID: 1, Handle: "pablo", HTML: "<p>brief</p>"})
	if short.Long {
		t.Fatal("a brief local post should not be clamped")
	}

	long := localView(&publish.Post{ID: 2, Handle: "pablo", HTML: "<p>" + strings.Repeat("word ", 300) + "</p>"})
	if !long.Long {
		t.Fatal("a long local post should be clamped")
	}
	if long.URL == "" {
		t.Fatal("a local post needs a permalink for the read-more link")
	}
}
