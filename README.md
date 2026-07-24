<img src="internal/web/assets/logo.png" alt="rss expert" width="200">

rss-expert
==========

A social reader for the open web. Reads feeds, publishes feeds, and threads
conversations over plain RSS. No API required to participate.

Single static binary, single SQLite file, single container. No JavaScript.

Status: 0.0.1. Everything below works and is covered by tests.


Build
-----

    go build ./cmd/rss-expert
    go test ./...

Requires Go 1.25 or later. No cgo, no Node, no build step for assets.


Run
---

    docker compose up -d

Compose publishes the app on 11081 by default; change RSS_EXPERT_HTTP_PORT if
that one is taken. Inside the container it is always 11080. That is the only
port: health, readiness, metrics and the status page all live behind it.

The first owner account is created from the environment on first boot. After
you have signed in, unset the password variable and restart -- it is only read
when there is no owner yet. There is no sign-up page: accounts are made by the
owner.


Environment
-----------

Everything is RSS_EXPERT_-prefixed. Only DOMAIN is required.

    DOMAIN            public hostname this instance publishes in its feeds
    HTTP_PORT         port compose publishes on the host  (11081)
    LISTEN            app address inside                 (:11080)
    DATA_DIR          database, uploads and backups      (data)
    ADMIN_EMAIL       owner account, first boot only
    ADMIN_PASSWORD    owner password, first boot only, 12 characters or more
    SMTP_URL          for sign-in links; empty means no mail is sent
    BEHIND_PROXY      trust X-Forwarded-* headers        (false)
    MEDIA_QUOTA_MB    uploads kept per account           (512)
    FETCH_LIMIT_MB    ceiling on any single fetch        (5)
    POLL_WORKERS      feeds fetched at once              (4)
    DB_CACHE_MB       SQLite page cache, writer          (20)
    METRICS_TOKEN     opens /metrics; empty means 404    (empty)
    SHOW_PREVIEW      serve /dev/preview                 (false)
    LOG_FORMAT        text or json                       (text)
    LOG_LEVEL         debug, info, warn, error           (info)

BEHIND_PROXY off means forwarded headers are ignored: nobody can claim an IP
address or an https scheme they do not have. Turn it on only when a proxy you
control sets them.


Commands
--------

    rss-expert serve                        run it
    rss-expert migrate                      apply the schema, then stop
    rss-expert doctor                       check the environment and the data
    rss-expert backup --into DIR            database plus uploads, incremental
    rss-expert restore --from DIR           into an empty data directory
    rss-expert restore --from DIR --check   verify without changing anything
    rss-expert sources add URL              subscribe from the shell
    rss-expert version


Operating
---------

    /healthz        answers "ok" to anyone, for an uptime monitor
    /readyz         database answering and schema current, no detail leaked
    /metrics        Prometheus format, only with METRICS_TOKEN
    /debug/heap     pprof heap profile, same token
    /admin/status   the same numbers as a page, for whoever is signed in

Nothing operational is public: the two probes carry no detail, and the rest is
404 until a token exists.


Interop
-------

In:   RSS 2.0, Atom, JSON Feed 1.0/1.1, h-feed, OPML, WebSub, rssCloud
Out:  per-user RSS and JSON, instance firehose, per-post comment feeds, OPML
Also: Webmention (send and receive), Micropub with a media endpoint, IndieAuth

Threads use source:inReplyTo with thr:in-reply-to (RFC 4685) as a fallback.
A post's guid is its bare permalink. Unknown XML elements are passed through
rather than dropped, as Textcasting requires.


Security
--------

Outbound fetches are checked after DNS and before connect, on every hop, so a
name that resolves to a private address is refused even mid-redirect. Uploads
are typed by their magic bytes, decoded before they are stored, and stripped of
EXIF and PNG text chunks without re-encoding a single pixel. Sessions and
tokens are stored hashed; passwords use argon2id. A TOTP code cannot be used
twice. Pages carry Content-Security-Policy default-src 'none', and there is no
script anywhere that would need it relaxed.


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

The way this thing looks owes a debt too:

  egg.design          The Starlight and Sunset pages by cornetespoir, whose
  (cornetespoir)      gradient skies, translucent panels, squared-off bubble
                      corner and ringed round badges are the visual language
                      this interface follows. Their repository carries no
                      licence, so none of their code is here -- the stylesheet
                      is ours. The feeling is theirs.
                      https://egg.design/
                      https://github.com/cornetespoir/page-faq

  Gerrit Halfmann     Pixelarticons, the 24x24 pixel icon set. MIT.
                      https://github.com/halfmage/pixelarticons

  Type                Fraunces, Atkinson Hyperlegible and JetBrains Mono, all
                      open licensed and served from this instance rather than
                      from someone else's CDN.

None of them endorse this. Interop bugs and broken real-world feeds we find go
back upstream.


Author
------

Pablo Murad <pablomurad@pm.me>
https://github.com/runawaydevil


License
-------

AGPL-3.0. See LICENSE.
