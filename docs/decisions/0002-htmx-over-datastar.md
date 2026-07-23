# ADR-0002 — HTMX, not Datastar

**Date:** 2026-07-23
**Status:** accepted

## Context

ADR-0001 commits to server-rendered HTML. Two hypermedia libraries were
plausible: HTMX 2.x and Datastar. Datastar began as a proposed HTMX rewrite,
folds in Alpine-style client reactivity, ships smaller, and drives updates over
SSE rather than AJAX.

## Decision

HTMX 2.0.9, vendored and served from `embed`. Datastar is rejected for now.

## Rationale

1. **We already have SSE from the stdlib.** `http.Flusher` gives us a push
   channel in roughly 40 lines. Datastar's main structural advantage is an
   SSE-first transport we get anyway, so it buys less here than elsewhere.
2. **Maturity asymmetry.** HTMX is a decade-old idea with a long stable line and
   a very large deployed base. Datastar is younger and moving faster. For a
   single-maintainer project with a hard "works without JavaScript" requirement,
   the boring dependency is the correct one.
3. **Client reactivity is not our problem.** Datastar's extra weight is signals
   and client-side state. This product is a reader: server state, server
   rendering, progressive enhancement. We would pay for a feature we designed
   ourselves out of needing.

## Consequences

- ~14 KB gzipped of JavaScript, inside the 30 KB per-page budget.
- No client-side reactive framework. Anything needing local interactivity gets a
  small hand-written island, or gets redesigned so it does not.
- Revisit if we ever need fine-grained client state — but §11 of the proposal
  makes that unlikely by design.
