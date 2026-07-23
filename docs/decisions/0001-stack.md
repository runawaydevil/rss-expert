# ADR-0001 — Go, templ, HTMX, SQLite: one binary

**Date:** 2026-07-23
**Status:** accepted

## Context

The product targets people who self-host small instances. RSC, our reference
project, needs a Docker Compose file with 4–5 services (Caddy, two Node
processes, Mailpit). For this audience, every extra service is one fewer
installation.

## Decision

Go 1.24+, stdlib `net/http` (method-pattern routing), `templ` for templates,
HTMX for interactivity, SSE for real-time, SQLite in WAL mode, everything
embedded in the binary via `embed`. No cgo.

Ships as a one-service `compose.yaml` and a `FROM scratch` image under 40 MB.

## Consequences

**We gain**

- No distro CVE surface: no `apt`, `apk`, glibc, or shell in the image. There
  is nothing to patch but our own binary.
- One process, one container. No supervisor, no `entrypoint.sh`.
- Cold boot under 1 s, and no Node build in the release pipeline.
- "Works without JavaScript" comes free: SSR is the native mode, HTMX enriches.

**We pay**

- No RSC code is reusable (it is TypeScript). Full rewrite.
- Smaller UI ecosystem than JS. We write more CSS by hand — which, given this
  project's design rules, is more benefit than cost.
- Without cgo the SQLite driver is either WASM or pure Go: see ADR-0003.

## Alternatives rejected

- **Fork RSC.** Upstream is mid-rewrite across four verticals with an atomic
  cutover. Forking today freezes the patient on the table. And forking a
  TypeScript project in order to rewrite it in Go is not a fork: it is a
  rewrite carrying someone else's git history and license obligations.
- **Node/TypeScript.** Large image, mandatory frontend build, and none of the
  three gains above survive.
- **An HTTP framework** (chi, echo, gin). Go 1.22+ stdlib routing covers method
  patterns. Less surface, less dependency churn.
