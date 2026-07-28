<img src="assets/logo-web.png" alt="rss expert" width="200">

rss-expert
==========

A social reader for the open web. Reads feeds, publishes feeds, and threads
conversations over plain RSS -- in real time, in both directions, with no API
required to participate. The timeline separates what was written here from what
arrived from elsewhere, and keeps where every post came from.

Single static binary, single SQLite file, single container. The only JavaScript
is a 20-line island that reveals a "new posts" banner; every page works without
it.

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
when there is no owner yet.

Who else may join depends on REGISTRATION: closed (the owner invites, one
address at a time), invite (anyone with an invitation), or open (anyone). Email
sign-in, confirmation and password recovery need SMTP_URL set; without it those
pages exist but send nothing, and no link is ever written to the log.


Environment
-----------

Everything is RSS_EXPERT_-prefixed. Only DOMAIN is required.

    DOMAIN            public hostname this instance publishes in its feeds
    HTTP_PORT         port compose publishes on the host  (11081)
    LISTEN            app address inside                 (:11080)
    DATA_DIR          database, uploads and backups      (data)
    ADMIN_EMAIL       owner account, first boot only
    ADMIN_PASSWORD    owner password, first boot only, 12 characters or more
    REGISTRATION      closed, invite or open             (closed)
    SMTP_URL          mail server for links; empty sends nothing
    MAIL_FROM         From address on those messages
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
    rss-expert sources remove ID            unsubscribe by source ID
                                            (the browser does this too, at /sources)
    rss-expert version


Behind a proxy
--------------

The timeline holds one long-lived connection to /events, so the proxy must not
buffer it. For nginx:

    location / {
        proxy_pass http://127.0.0.1:11081;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }

    location /events {
        proxy_pass http://127.0.0.1:11081;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Connection "";
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 1h;
    }

The app also sends X-Accel-Buffering: no on that endpoint, which nginx honours
on its own -- the explicit block is belt and braces. Set BEHIND_PROXY=true so
the forwarded headers are trusted.


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
Also: Webmention (send and receive), Micropub with a media endpoint,
      domain as identity (rel=me and h-card, verified both ways),
      ActivityPub, off by default

Push works both ways. As a subscriber it discovers a feed's hub or cloud, keeps
the subscription renewed, and takes deliveries verified by their signature. As a
publisher it is its own hub and its own cloud: it advertises both in every feed
and tells its subscribers the moment something is published. Feeds it cannot get
by push are polled on an adapting schedule with conditional requests.

Set RSS_EXPERT_ACTIVITYPUB and @handle@yourdomain becomes an address the
fediverse can follow: WebFinger, an actor document, a signed inbox, and one
Create per follower when you publish. The outbox carries recent Create
activities, and signed Note replies to a local post enter the same thread as
replies learned from feeds. The actor that answered is recorded as provenance,
not as a subscription: it never joins the list of feeds this instance follows
and is never polled as though it were one. Answering a remote post from here is
still to come. Off means the routes do not exist, not that they answer
politely.

Settle the domain before turning it on. Actor, key and post identifiers are
absolute URLs on it and every receiver enforces origin matching, so moving
afterwards kills the actor: old followers point at an address that answers
nothing, and an edit to an old post arrives as a stranger claiming somebody
else's identifier. Post guids are never rewritten, including on a move.

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
twice. The public federation endpoints -- the hub, the cloud registration,
Webmention, Micropub and the fediverse inbox -- are rate limited per address, so a stranger cannot
use them to hammer a third party or to fill the disk. Pages carry
Content-Security-Policy default-src 'none' plus script-src 'self'; the only
script is the twenty-line island, served from this origin, and no page carries
an inline one.


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
  W3C                 ActivityPub and ActivityStreams 2.0.
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
