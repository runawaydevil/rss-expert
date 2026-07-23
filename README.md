rss-social
==========

A social reader for the open web. Reads feeds, publishes feeds, and threads
conversations over plain RSS. No API required to participate.

Single static binary, single SQLite file, single container.

Status: pre-alpha. Nothing here works yet.

See docs/PROPOSTA.md for the full design (Portuguese).


Build
-----

    go build ./cmd/rss-social
    go test ./...

Requires Go 1.24 or later. No cgo, no Node, no build step for assets.


Run
---

    docker compose up -d
    docker compose exec rss-social /rss-social admin create --email=you@example.org

The app listens on 11080. The admin panel listens on 127.0.0.1:11090 and is
not published by compose; reach it over an SSH tunnel.

There is no default password and no web path to becoming an administrator.
The CLI on the server is the only way in, and the only way back.


Interop
-------

In:   RSS 2.0, Atom, JSON Feed 1.0/1.1, h-feed, OPML, WebSub, rssCloud
Out:  per-user RSS and JSON, instance firehose, per-post comment feeds, OPML

Threads use source:inReplyTo with thr:in-reply-to (RFC 4685) as a fallback.
A post's guid is its bare permalink. Unknown XML elements are passed through
rather than dropped, as Textcasting requires.


Credits
-------

Independent implementation. No code was copied from any other project, but the
interop knowledge that makes it possible came from other people's work:

  Dave Winer          RSS 2.0, OPML, rssCloud, Textcasting, the source:
                      namespace, and rss.chat -- including its hoist/dehoist
                      idea, which this project borrows.
                      https://scripting.com/

  Ricardo Mendes      RSC, the proof that this works at all, and the interop
                      map we studied.
                      https://github.com/rmdes/rsc

  IndieWeb            Micropub, Webmention, IndieAuth, microformats2.
                      https://indieweb.org/

  JSON Feed           Manton Reece and Brent Simmons.
                      https://www.jsonfeed.org/

None of them endorse this. Interop bugs and broken real-world feeds we find go
back upstream.


License
-------

AGPL-3.0. See LICENSE.
