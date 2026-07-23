# The `source:` namespace, as observed on the wire

Written from two feeds captured 2026-07-23 and kept in `testdata/feeds/`:
`scripting.com/rss.xml` (136 KB) and `rss.chat/users/rss.xml` (114 KB). Where
this document and `docs/PROPOSTA.md` disagree, this one is right: it was read
off the wire, the proposal was written from specs.

## The namespace URI is https, not http

Both reference implementations declare:

    xmlns:source="https://source.scripting.com/"

The proposal says `http://source.scripting.com/`. Getting this wrong means
every `source:` element is silently invisible — the feed parses, the markdown
is not found, threading does not resolve, and nothing errors.

We accept both URIs on input and normalise the legacy `http://` form to the
`https://` one at decode time. We emit `https://`.

## Channel level

    <source:account service="bluesky">@scripting.com</source:account>
    <source:account service="mastodon">@davew@mastodon.social</source:account>
    <source:account service="twitter">bullmancuso</source:account>
    <source:self>http://scripting.com/rss.xml</source:self>
    <source:localTime>Thu, July 23, 2026 12:15 PM EDT</source:localTime>
    <source:blogroll>https://feedland.social/opml?screenname=dave</source:blogroll>

`source:account` is channel-level, confirming the proposal — but it is
**repeated, one per service, and carries a `service` attribute**. It is not the
single text element the proposal describes. We model it as a list.

`source:self` is the feed's own address. `source:localTime`, `source:blogroll`
and `source:outline` we do not model; they pass through untouched.

## Item level

    <guid>https://rss.chat/?id=385</guid>
    <source url="https://rss.chat/users/dave/rss.xml">Dave Winer</source>
    <source:markdown>text in **markdown**</source:markdown>
    <source:inReplyTo>https://rss.chat/?id=382</source:inReplyTo>
    <source:comments count="1" feedUrl="https://rss.chat/users/dave/comments/382.xml"/>

Note the collision: plain `<source>` is the core RSS element naming the
originating feed, and has no namespace. `<source:markdown>` is ours. Matching
on prefix rather than namespace URI confuses them; matching on URI does not.

In the 100-item firehose: 94 items carry `source:markdown`, 65 carry
`source:inReplyTo`, 50 carry `source:comments`. The parser test asserts those
exact counts against the frozen corpus.

## Rules that matter

1. `guid` is the bare permalink and is the thread key. `isPermaLink` defaults
   to true when absent.
2. `source:comments/@feedUrl` is an RSS feed of the replies to that one post.
   A thread is walked one feed at a time, every level the same shape, with no
   API involved.
3. `source:markdown` is the detection marker for a Textcasting peer.
4. Unknown elements are relayed, not dropped.
