# DB Contest design

Everything the frontend is built from lives here. `SPEC.md` is the source of
truth; `preview.html` illustrates it and is reproducible from it.

| File | What it is |
|---|---|
| [`SPEC.md`](SPEC.md) | the specification: tokens, typography, states, components, order of work |
| [`preview.html`](preview.html) | a visual mockup of the key screens; opens in a browser as it is |
| [`CREDITS.md`](CREDITS.md) | sources and licences for the images |
| `assets/` | the image files |

## Looking at the mockup

```bash
open docs/design/preview.html
```

The page follows the operating system's theme: switch between light and dark and
everything rebuilds except the comparison blocks, which are pinned on purpose.

## The accepted direction

*Chistovik* ("fair copy") — chosen from three worked-out options. The palette
comes from the *Kartoteka* ("card index") option; the method of layout and
typography was taken from the Tailwind CSS documentation by reading the computed
styles of the live page rather than from memory.

Three findings decided the result and are invisible in a screenshot: the heading
is set at **weight 400**, not 700; tracking is −0.05em against leading 0.98; and
the alpha of the rules between themes is **doubled, not mirrored** — 5 % black on
white against 10 % white on black.

## The revision of 2026-08-26

The specification was revised once, after the first build of live screens. Three
things changed, each because the original value worked worse on a real monitor
than it did on paper:

- **The content column** — up to 1760 px instead of a fixed 1136, with hatched
  fields at `clamp(24px, 4.5vw, 140px)`. The fixed 1136 px came from the Tailwind
  documentation, where a column holds prose; here it holds a register, and on a
  1920 px monitor a third of the screen went to hatching.
- **The type scale** — raised by roughly 9 % throughout, with the steps `row`,
  `control` and `control-sm` added. Captions at 11.5 px read as fine print.
- **The sign-in screen** splits into two columns from 1280 px, not from 760.

The reasoning for each is in the specification, next to the value itself.

## What is checked automatically

Three checks, all in CI (`.github/workflows/frontend.yml`):

- **Contrast** ≥ 4.5:1 for text and ≥ 3:1 for the boundaries of interactive
  controls, in both themes — `frontend/scripts/contrast.mjs`, over the tokens
  rather than by eye. On draft values it caught three failures that were
  invisible.
- **An ESLint rule** against arbitrary colours (`bg-[#…]`), arbitrary type
  (`text-[13px]`, `tracking-[…]`) and weight 700 — in plain strings and in
  template literals alike.
- **Tests for `cn()`**, proving that a step of this project's own scale survives
  a colour class written beside it. Without them `tailwind-merge` reads
  `text-label` as a colour and silently drops it, and the caption renders in the
  right family at the wrong size.

## Relation to the architecture

The specification rests on [`../ARCHITECTURE.md`](../ARCHITECTURE.md): section
6.1 (question modes and the hidden question) and section 6.2 (multilingual text
and the three layers). The reverse edit — a section on frontend architecture — is
described in section 17 of the specification and has not been made yet.
