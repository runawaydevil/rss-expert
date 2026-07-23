# ADR-0003 — modernc.org/sqlite, not ncruces

**Date:** 2026-07-23
**Status:** accepted

## Context

ADR-0001 rules out cgo, which leaves two SQLite drivers: `ncruces/go-sqlite3`,
which runs the real SQLite compiled to WebAssembly, and `modernc.org/sqlite`,
which is SQLite machine-translated to Go. `docs/PROPOSTA.md` picks `ncruces` on
the strength of published benchmarks and flags its memory use as the risk to
measure before committing. So we measured.

## What was measured

A workload shaped like ingestion: a table resembling `observation`, 20 000 rows
inserted in one transaction, then 20 000 indexed point reads across 4
concurrent connections, five rounds, best round reported. Identical DSN pragmas
on every driver — `journal_mode(WAL)`, `synchronous(NORMAL)`,
`busy_timeout(5000)`, `cache_size(-20000)`, `temp_store(MEMORY)` — and verified
applied by reading each pragma back after open. Peak working set via
`GetProcessMemoryInfo`. Windows, `CGO_ENABLED=0`.

| | ncruces v0.33.2 | ncruces v0.35.2 | modernc v1.54.0 |
|---|---|---|---|
| FTS5 without build tags | yes | **missing** | yes |
| Peak RSS | 50–63 MB | 51–57 MB | 82–102 MB |
| Writes/s | ~135 000 | ~134 000 | **~156 000** |
| Reads/s | ~172 000 | ~164 000 | ~170 000 |
| Binary | 18.6 MB | 14.7 MB | **9.6 MB** |
| Failures | intermittent | intermittent | **none** |

## Decision

**`modernc.org/sqlite`.**

Three findings decided it, none of them the one the proposal expected.

**The read advantage does not exist in our workload.** The proposal cites
`ncruces` as roughly 3× faster on reads. On indexed point queries through
`database/sql` the three are within noise of each other, and `modernc` is
actually the fastest writer. The published benchmarks measure something we do
not do.

**`ncruces` fails intermittently under ordinary concurrency.** Both versions
produced `sqlite3: invalid _pragma: sqlite3: database is locked` when the pool
opened connections while a write was committing — zero to two failures per run,
never reproducible on demand. The cause is structural: DSN pragmas are applied
at connection open, and `busy_timeout` is itself one of the pragmas being
applied, so it is not yet in effect to absorb the lock it needs to survive.
`modernc` never failed, in any run. An intermittent connection failure is worse
than a slow one; it turns into a random 500 in production and an unreproducible
bug report.

**The live branch of `ncruces` lost FTS5.** v0.35.2, the current release, cannot
create an FTS5 table without extra build configuration. v0.33.2 can, but it is
the tail of the abandoned wazero line — choosing it means pinning to a branch
that will not get fixes. Search is a Phase 2 requirement, so this is not a
detail we can defer.

## Consequences

- We pay roughly 40 MB of peak RSS. Most of it is page cache we explicitly
  asked for: 4 connections × 20 MB. It is a tuning knob, not a floor — the
  writer pool gets the large cache and the reader pool a smaller one, and
  §4.4.1's budget is enforced in CI. The lock failure was not a knob.
- The binary is 9 MB smaller, which is real against a 40 MB image ceiling.
- `mmap_size`, left open in the proposal, is moot: `modernc` is pure Go and does
  not memory-map the database the way the C library does. Nothing to tune.
- If memory becomes the binding constraint at 500 sources, the first move is
  cache tuning, not a driver swap. Revisit only if the budget fails after that.

## Reproducing

The harness is not in this repository — the three candidates need conflicting
module versions, so it lives as three throwaway modules. Rebuild it by pinning
each driver in its own module with `go mod edit -require`, applying the pragmas
above via DSN, and reading them back to confirm they took effect. That last step
matters: an earlier run of this comparison was invalid because `go mod tidy`
had quietly upgraded the pinned version to the one it was being compared against.
