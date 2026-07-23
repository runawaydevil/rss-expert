# ADR-0005 — One column, pixel icons, a borrowed sky

**Date:** 2026-07-23
**Status:** accepted

## Context

Three attempts. The first was austere — hairline rules, warm grey, a dusty blue
accent, serif body text. It read as anemic: the right structure with no
personality, and it looked like a document template rather than a place anyone
would want to spend time.

The second took its cue from [rsc.rmdes.be](https://rsc.rmdes.be/) and
[textcasting.org](https://textcasting.org/). Reading RSC's actual stylesheet
settled a question the screenshots could not: `--font-heading: "Libre Bodoni"`
with `--font-body: "Public Sans"` over Tailwind's zinc scale and an orange
accent. The character comes entirely from the display serif; everything under it
is default. That version was better and still wrong — a cream page with four
coloured rails, four kinds of badge, three sidebar panels, tabs, density
switches. Too much furniture for a thing whose job is reading.

## Decision

**One reading column, and the sky from egg.design.**

`cornetespoir`'s Starlight and Sunset pages supplied the visual language:

- a fixed gradient ground rather than a flat fill — Sunset runs
  `#74819D → #FCA699 → #F8D28B`; ours is lavender to peach to gold in the day
  and Starlight's `#201b33` indigo at night
- translucent panels floating on that ground instead of opaque cards
- one corner of each card squared off, so the shape points back at whoever spoke
- round badges with a thick ring that breaks the edge they sit on

Their repository carries no licence, so **none of their code is used**. This is
the same line the project already draws around RSC: take the knowledge, write
the implementation, say whose idea it was. The README says so and so does the
page footer.

Icons are Pixelarticons (MIT), on a 24×24 pixel grid, inlined so they inherit
`currentColor` and cost no extra request. Pixel art because it is the small
web's own visual signature, and because egg.design reaches for a pixel typeface
for the same reason.

Type stays as chosen: **Fraunces** for display, **Atkinson Hyperlegible** for
reading, **JetBrains Mono** for anything the system observed rather than wrote.

## What was removed, and why

Three columns, scope tabs, sidebar panels, per-kind colour rails, category
pills, source-health notices, the density switcher, and the token catalogue. All
of it was information about the reader rather than the thing being read. The
product is a place to read; everything that competes with the text loses.

`app.css` went from 11.4 KB to 4.8 KB in the process.

## Consequences

- Provenance, which the whole architecture exists to preserve, now gets one
  quiet line per post instead of a five-part strip. It is still there, still
  first-class in the data, but it no longer shouts.
- Source health, hoist/dehoist and collections need somewhere to live when their
  phases arrive. They do not get to move back into the reading column by
  default; each one has to earn its place.
- The palette keeps more hues than the interface currently spends. They are not
  dead weight — Phase 2 gives kinds of content distinct presentations, and this
  is where those come from.
