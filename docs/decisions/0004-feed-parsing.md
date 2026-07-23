# ADR-0004 — gofeed for the mess, our own pass for the namespace

**Date:** 2026-07-23
**Status:** accepted

## Context

Two jobs look like one. Normalising RSS 2.0, RSS 1.0, Atom and JSON Feed —
including the large fraction of real feeds that are malformed — is a solved
problem with a long tail of edge cases nobody should re-solve. Reading the
`source:` namespace correctly is our differentiator and has to be exact.

`gofeed` does the first job well. It cannot do the second:

- `ext.Extensions` is keyed by **namespace prefix**, not namespace URI. A feed
  declaring `xmlns:src="https://source.scripting.com/"` would be filed under
  `src` and silently missed.
- `ext.Extension.Children` is a `map[string][]Extension`, so sibling order is
  lost. Faithful pass-through (Textcasting's requirement that an app relay
  elements it does not understand) cannot be built on an unordered structure.

## Decision

Parse every payload twice.

1. `gofeed` produces the universal model: titles, links, dates, authors,
   enclosures, and all the tolerance for broken markup.
2. For XML payloads, a second pass with `encoding/xml` builds an ordered
   element tree with namespaces resolved to URIs, and overlays onto the model:
   `source:markdown`, `source:inReplyTo`, `source:comments`, `source:account`,
   `source:self`, the RFC 4685 `thr:in-reply-to` fallback, `guid/@isPermaLink`,
   and every unmodelled namespaced element, kept in order, for pass-through.

The two passes are joined by item key — `guid`, then `link`, then position.

## Consequences

- Two parses of a payload capped at 5 MB. Measured against the corpus this is
  not where ingestion time goes; polling and I/O are.
- The pass-through tree is structural, not raw bytes. We re-emit through
  `encoding/xml` with our own namespace declarations, so a relayed element
  cannot smuggle markup or reference an undeclared prefix. Byte-exact relay was
  rejected for that reason.
- A failure in the second pass is not fatal. `gofeed` already produced a usable
  feed, and the three-layer store keeps the raw payload, so a parser fix can
  reprocess history rather than lose it.
