# DB Contest design system — specification

Status: accepted 2026-08-25, revised 2026-08-26 after the first run on real
screens. Basis for the "frontend skeleton" step and a precondition for item 3 of
the implementation plan (`docs/ARCHITECTURE.md`, section 14).

The direction was chosen from three worked-out options. The third was accepted —
*Chistovik* ("fair copy"): the palette comes from the *Kartoteka* ("card index")
option, and the method of layout and typography was taken from the Tailwind CSS
documentation by reading the computed styles of the live page rather than from
memory.

## 1. Scope of this step

**In:** the token layer, the type scale, density modes, the state taxonomy, the
component inventory, the `frontend/` directory structure, the `/design`
showcase, and a vertical slice on the sign-in and contest-listing screens.

**Out:** content CRUD and the contest constructor — that is item 3 of the
architecture, and it begins once the system is ready and is assembled from its
parts.

**Precondition met:** the branch is merged with `main`, and the `frontend/`
skeleton and architecture sections 6.1/6.2 are in the tree.

## 2. Principles

Five rules, from which the rest follows. Each one decides something later.

1. **Data outranks chrome.** The maximum number of pixels goes to the result
   table, the editor and the schema tree. The interface around them is rules and
   typography.
2. **Hierarchy is carried by scale and weight, not by boldness.** The large
   steps run at weight 400 with strong negative tracking. There is no 700 in the
   system.
3. **Motion never stands between an action and its result.** The product runs
   under a timer; an animation that adds 300 ms of waiting spends the
   participant's time.
4. **A state is always explained.** No empty screen without a reason and a next
   step, no error without what to do about it.
5. **One accent, three semantics.** Widening the palette is a change to the
   system, not a decision taken on the spot. It has been widened twice, each
   time as a named sub-palette with one job: SQL highlighting (section 3.1) and
   the standings (section 3.5).

## 3. The token layer

Three levels, each referring only to the one below it: primitive → semantic →
component. It lives in `frontend/styles/tokens.css` and is exposed through
Tailwind v4's `@theme` in `app/globals.css` — one source produces both the CSS
variables and the utilities.

**The rule that makes the system checkable:** the dark theme redefines the
semantic level **only**. Primitives and component tokens are not duplicated. If a
component token ever has to be touched for the sake of the dark theme, the
semantics are described wrongly, and the semantics are what gets fixed.

### 3.1 Semantic tokens

| Token | Light | Dark | Purpose |
|---|---|---|---|
| `--color-bg` | `#ffffff` | `#0a0b0f` | the page ground |
| `--color-panel` | `#fafafa` | `#101218` | raised surface, demo panel |
| `--color-sunk` | `#f6f6f6` | `#0d0f14` | sunken area (editor) |
| `--color-ink` | `#0d1117` | `#ffffff` | primary text |
| `--color-ink-2` | `#4c535e` | `#a6adb8` | secondary text |
| `--color-ink-3` | `#66696d` | `#898c8c` | muted text, captions |
| `--color-line` | `rgb(13 17 23 / 0.07)` | `rgb(255 255 255 / 0.10)` | separating rule |
| `--color-line-2` | `rgb(13 17 23 / 0.13)` | `rgb(255 255 255 / 0.17)` | stronger rule |
| `--color-edge` | `#838587` | `#6c6f70` | **boundary of an interactive control** |
| `--color-pattern` | `rgb(13 17 23 / 0.05)` | `rgb(255 255 255 / 0.10)` | hatched fields, dotted grid |
| `--color-ann` | `rgb(13 17 23 / 0.24)` | `rgb(255 255 255 / 0.26)` | decorative annotation caption |
| `--color-cta-bg` | `#0d1117` | `#262b36` | ground of the primary action |
| `--color-cta-fg` | `#ffffff` | `#ffffff` | text of the primary action |
| `--color-accent` | `#0e5048` | `#5ed0bc` | the accent — only for what is live |
| `--color-accent-wash` | `#e4efec` | `rgb(94 208 188 / 0.12)` | tinted surface under the accent |
| `--color-good` | `#2c6338` | `#6fbf80` | accepted, success |
| `--color-warn` | `#8a5510` | `#d59a3e` | warning |
| `--color-bad` | `#a32a21` | `#e0796d` | rejection, error |

SQL highlighting is a sub-palette derived from the main one: a keyword carries
the accent, a function `#1f4e79`/`#7fb2e5`, a string `#8a5510`/`#d5a75e`, a
number `#8f3a14`/`#e0956b`, a comment `--color-ink-3`.

### 3.2 Three colour decisions worth not losing

**The light theme stands on pure white, not on tinted paper.** The whole method
rests on a rule at 5 % — on a cream or grey ground that rule drowns.

**Alpha values are doubled between themes, not mirrored.** 5 % black on white
against 10 % white on black. A dark ground swallows contrast harder than a light
one; an honest inversion produces a rule nobody can see.

**A control's boundary is a separate token from a decorative rule.** They answer
to different WCAG thresholds: a rule has none at all, while the outline of an
interactive control needs 3:1 (SC 1.4.11). One token doing both jobs would fail
the stricter threshold — which had already happened in two drafts running.

### 3.3 No arbitrary colours

`bg-[#…]`, `text-[#…]`, `border-[#…]` are forbidden and are caught by an ESLint
rule. Without it the system spreads out in about a month. The single source is
`tokens.css`; there is no `tailwind.config` holding colours.

### 3.4 Switching the theme

The theme lives in a cookie (`dbcontest_theme`: `system` / `light` / `dark`) and
is read by the server before anything renders; `<html>` carries `data-theme` in
the first byte of markup. `system` writes no attribute at all — that absence is
exactly what lets the `prefers-color-scheme` block in `tokens.css` apply.

This replaces the blocking inline script that every second dark-mode
implementation ships, whose only job is to repaint a page that has already been
shown wrong. There is nothing to flash here: every route is dynamic anyway
because each one reads the session, so reading one more cookie costs nothing.

The dark values are declared **once** (`--dark-*`) and referenced from two
places: under the media query and under the explicit choice. Repeating twenty
values across two blocks is precisely the mechanism by which they diverge — one
copy gets corrected and the other keeps the old value until somebody notices in
the wrong theme.

### 3.5 The standings sub-palette

The leaderboard is the one screen read for its colour before its numbers: who
is on the podium, which row is yours, whether the table is frozen. Those three
questions get colour, and nothing else on that screen does. The palette lives in
`tokens.css` beside the rest and is checked by the same contrast script — every
hue as text on its own wash and on the page ground, in both themes.

| Token | Light | Dark | Job |
|---|---|---|---|
| `--gold` / `-wash` | `#7a5600` / `#fbf0d2` | `#e6c061` / 14 % | place 1, the winner |
| `--silver` / `-wash` | `#4e5865` / `#ebeef2` | `#c3cbd5` / 12 % | place 2 |
| `--bronze` / `-wash` | `#8a4318` / `#f8e8dd` | `#e8a070` / 14 % | place 3 |
| `--gold-fill` / `-on`, `--silver-fill` / `-on`, `--bronze-fill` / `-on` | `#e2b53c` / `#2e2000`, `#c2cad4` / `#1f2731`, `#d88e57` / `#331704` | the dark medal colours / `#0a0b0f` | the solid medal: its disc, its score bar, its podium edge, and the number on it |
| `--frost` / `-wash` | `#0b5c77` / `#e2f1f7` | `#7dd0ea` / 12 % | a frozen table |
| `--you` / `-wash` | `#1e4a8e` / `#e7eefa` | `#97b6f3` / 13 % | the participant's own row |
| `--id-1`…`--id-6` / `-wash` | rose, ochre, olive, cyan, ocean, sienna | the same, lightened | a participant's initials and score bar |

Rules that keep it a palette rather than decoration:

- **Medals only for places 1–3.** A shared place shares the medal; place 4 is
  ink like any other number. A medal has two colours on purpose: `--gold` is
  held to 4.5:1 as text on white, and a gold that dark reads as brown on a
  disc, so the disc takes `--gold-fill` and the number on it `--gold-on`.
- **Frost is not the accent.** The accent means "live", and a frozen table is
  the opposite of live, so the two must never be confusable. A live table
  carries the accent's pulsing dot; a frozen one carries frost.
- **An identity hue is chosen by a hash of the label**, so a person keeps their
  colour between reads and between the tab and the public page. It is drawn
  only as a disc of initials and as the fill of the score bar — a shape nothing
  else in the system uses, which is what keeps rose from reading as an error and
  olive as success.
- **Colour is never the only carrier.** A medal also has its number, the frozen
  state its sentence, your row the word "you", a winner the word "winner".
- **The ICPC grid reuses `good`/`warn`/`bad`, not a fourth family.** A solved
  cell is `good` text on `good-wash`, with the attempt and the minute written
  on it; the first solve of a question is a solid `good` fill with the page
  ground as its text, the same construction as a medal disc, so it still reads
  from across a hall. A failed cell is `bad` on `bad-wash` with its wrong-attempt
  count; a frozen table's unresolved-but-attempted cell is `warn` on
  `warn-wash`, with a question mark over how many attempts came after the
  freeze and, only when there were any, a second "−N" mark for the wrong
  attempts already known before the freeze — that count was on screen before
  the table ever froze, so repeating it leaks nothing about what happened
  after; an untried cell carries no colour at all. Every cell also carries a
  spelled-out accessible name — the question's letter and its state in words,
  naming both counts on a pending cell when both apply — so none of this
  rests on colour or on a bare symbol.
- The section 15 boundaries still hold: no purple, no glow, no gradient text, no
  emoji. A medal is a tinted disc with a number in it, not a trophy picture.

## 4. Typography

`Onest` — the interface. `JetBrains Mono` — SQL, every number, utility captions.
`Literata` — the text of the crime story and nowhere else.

Coverage was checked empirically, by measuring glyph width against substitution:
all three faces have their own `Ș ș Ț ț Ă ă` (comma below, as Romanian
orthography requires, not cedilla) and full Cyrillic. The `latin-ext` subset
Google declares is identical for every font and guarantees nothing by itself.

| Step | Size / leading | Weight | Tracking | Measure |
|---|---|---|---|---|
| `display` | `clamp(44px, 8vw, 104px)` / `0.98` | 400 | `-0.05em` | 20ch |
| `h2` | `clamp(32px, 3.6vw, 52px)` / `1.02` | 400 | `-0.042em` | 20ch |
| `h3` | `21px` / `1.3` | 500 | `-0.02em` | — |
| `lede` | `20px` / `1.55` | 500 | — | 60ch |
| `body` | `17px` / `1.65` | 400 | — | 66ch |
| `small` | `14px` / `1.5` | 400 | — | — |
| `row` | `17.5px` / `1.35` | 500 | `-0.022em` | — |
| `control` | `15px` / `1.4` | 500 | — | — |
| `control-sm` | `13.5px` / `1.4` | 500 | — | — |
| `label` (mono) | `12.5px` / `1.5` | 500 | `0.1em`, uppercase | — |
| `data` (mono) | `13px` / `1.85` | 400 | — | `tabular-nums` |
| `narrative` (Literata) | `18px` / `1.7` | 400 | — | 58ch |

**The scale was revised upward after the first run on real screens.** The first
set of values was taken from the Tailwind CSS documentation and inherited its
sizes — but that is a page made of prose, and this is a register, a query log and
a result table, read close up and for a long time. At 1600 px, captions at
11.5 px and body at 15.5 px read as fine print. Roughly 9 % across the whole
scale keeps the relationships between the steps and gives back a normal size.

**The steps `row`, `control` and `control-sm` were added to the original nine.**
Their absence was a hole, not an economy: the title of a register row and the
text of a button exist on every screen, and the specification did not have them —
the button's size was written in section 5 as a property of one pill. So every
button and every row arrived with a size written on the spot. A step in the scale
is what makes writing one unnecessary.

**Each step carries size, leading, tracking and weight as one value.** In code
that is Tailwind v4's `@theme` (`--text-display`, `--text-display--line-height`,
and so on), so `text-display` cannot be taken without its tracking. Tailwind's
stock scale is **erased** (`--text-*: initial`): `text-xl` does not exist, and a
size can only be picked from the system. A tenth step is an edit to
`app/globals.css`, not a decision taken alone inside a component.

**The trap that cost one run.** `text-*` is two different utilities under one
prefix: a size and a colour. `tailwind-merge` tells them apart by looking the
name up in Tailwind's stock size list, so a step this project invented
(`text-label`) is classified as a colour, collides with the `text-ink-3` beside
it and is silently dropped. The caption renders in the right family and the wrong
size — a defect that survives review. The cure is extending `cn()` in
`lib/utils.ts`, where the scale is declared explicitly; there is a test for it.

**The monospace rule:** if a value can be compared to its neighbour by looking
down the column, it is set in `JetBrains Mono` with tabular figures. Proportional
figures in a column produce a column that cannot be read by scanning.

**Why weight 400 on the large steps.** Scale gives the hierarchy; a light weight
gives the calm back. On Cyrillic this works harder than on Latin: it has a higher
density of vertical strokes, and a heavy large grotesque turns into a palisade.

## 5. Layout

**Hatched fields.** The strips to the left and right of the content column are
hatched at 315° in one pixel on a 10 px step (`--color-pattern`) and bounded by a
rule. This holds the column without a single frame. The implementation is a
`repeating-linear-gradient` on the outer tracks of the band's grid.

**The one deliberate exception: the play workspace.** `/contests/[contestId]/play`,
once a contest is running, renders full-bleed — no hatched fields, no content
column, no `Band` at all. Every other screen in the product holds this rule
because principle 1 of section 2 ("the maximum number of pixels goes to the
data") is served by *bounding* the data's width, the way a register or a
result table reads better at 1760 px than edge to edge on a wide monitor. The
play workspace inverts that trade-off rather than breaking it: during an
olympiad the "data" is the console, the result, the query log and the
questions all at once, competing for the same screen the way panes in an
editor do, and a hatched field on either side would be pixels taken from
that competition and given to a decoration. The exception is scoped
narrowly — it applies to this one route in its one "the contest is running"
state, not to the waiting room or the unavailable screen either side of it,
both of which are ordinary content pages and keep `Band` like everything
else. See `frontend/app/(participant)/contests/[contestId]/play/workspace.tsx`
for the layout itself.

**Inside that grid, three panels collapse like VS Code's.** The schema tree
on the left, the side panel on the right (story, questions, notes, table —
in that order) and the bottom panel (result, query log) each carry a toggle
in the play header, with `aria-pressed` and the shortcut in its `title`:
Ctrl/⌘+B for the left panel, Ctrl/⌘+Alt+B for the right, Ctrl/⌘+J for the
bottom, all three bound in the editor's own CodeMirror keymap too, ahead of
the window listener, so a shortcut works with the caret sitting in the
editor rather than typing "b" or turning text bold. A collapsed panel does
not sit empty in its track — it and the divider beside it leave the grid
entirely, and the editor takes the freed width. Collapsing all three leaves
only the tab strip and the editor on the screen. A finished run reopens a
collapsed bottom panel on its own, the same reasoning that already switches
its tab to Result: the participant is meant to see what the query did.
Below 760 px the same panels are stacked sections rather than columns, and
the same toggles hide the same sections. What is collapsed is remembered in
`localStorage` per contest, beside the pane widths (section 12's directory
note on `lib/`).

**The editor carries its SQL as a strip of tabs, VS Code's own shape:** a
name, a close cross, and a trailing "+", double-click or F2 to rename (Enter
commits, Esc cancels), drag to reorder, arrow keys to move between them as a
`tablist`. Each tab keeps its own CodeMirror document — its own undo
history, its own cursor — so switching tabs swaps state rather than text,
and typing still repaints nothing above the editor. Running (the button or
⌘↵) always runs the active tab's text, and the result names the tab it came
from, because a result outlives the tab switch that follows it.

**A result row opens in full underneath the table, not beside it.** The
result table's columns are capped in width, which is exactly what makes a
long value in one of them unreadable; clicking a row (or pressing Enter on
one — rows are focusable) opens a detail panel below the table with every
column spelled out, long values wrapped, monospace for numbers and dates,
the same `NULL` glyph the table uses. "Copy value" sits beside each column,
"Copy row" copies it the way the CSV export would, and the up/down arrows
move to the neighbouring row without closing the panel. A new run closes it;
the query log is untouched by this — its rows do not expand, because a log
line is a record of what ran, not a place to re-read one value.

**Both split points in the console — table/detail-panel and
editor/bottom-panel — are dragged, not fixed.** Each is `PaneHandle` on the
vertical axis: `aria-orientation="horizontal"`, arrow keys move it, a
pointer drag writes the size straight onto the container without going
through React, and the size is remembered per contest exactly as the
horizontal pane widths already were (`usePaneWidths`'s own module,
generalised to a group of stored sizes rather than copied for a second
axis). The editor/bottom-panel split defaults to 55/45, the same 11:9 ratio
the grid shipped with when it was still fixed.

**The content column is up to 1760 px, the fields are
`clamp(24px, 4.5vw, 140px)`.** The former fixed 1136 px came from the Tailwind
CSS documentation, where a column holds prose. Here it holds a register, a query
log and a result table, and on a 1920 px monitor a fixed column violated
principle 1 of section 2 ("the maximum number of pixels goes to the data")
outright: a third of the screen went to hatching. The fields are defined not by a
minimum but by a dependence on the window's width — at 16 px they read not as a
margin but as a rendering artefact.

The text measure does not grow with it: `body` is still 66ch, `lede` 60ch,
`narrative` 58ch. Tables get the width, paragraphs do not.

**Rounding lives on the outer frame only.** A demonstration window is 10–12 px;
inside the window there are no nested plates, and panels are separated by a rule.
A text field is **square**: it is neither the outer frame nor a small control,
and the register this direction is has cells with corners. Tailwind's stock
radius ladder is erased from the theme (`--radius-*: initial`), leaving two named
radii (`window`, `frame`) plus `rounded-none` and `rounded-full`. The
`rounded-md` somebody reaches for when a panel "looks bare" simply does not
exist.

**The primary action is a pill.** Radius 999 px, 15 px at weight 600, ground
`--color-cta-bg`. A tiny button beside a large heading: the contrast of scales
does the work that colour is usually asked to do.

**Density — two modes.** `comfortable` (profile, constructor, narrative) and
`compact` (result table, query log, monitoring, leaderboard). They are switched
by a `data-density` attribute on a container, which overrides the component
tokens: `--control-h` (38 px against 32), `--control-px`, `--row-py`, `--row-px`,
`--stack-gap`. The same `DataTable` works in both, with no second component and
no prop threaded through four layers.

**The mobile reset.** Below 760 px the hatched fields are hidden, the column
takes the full width, and every asymmetric grid collapses to a single column.
What collapses is the **band's template** — to one track, not to three of zero
width: a hidden grid item leaves the flow entirely, and with three tracks the
content slides into the first one and renders at 104 px inside a 375 px screen.

**On a narrow screen the register loses columns, not rows.** Restacking a table
into cards is the usual answer and the wrong one here: it dissolves the columns,
and comparing down a column is the only reason this is a register and not a wall
of cards. So enrollment and format fold into a caption under the title, while
state and the window stay — those are what a reader comes to a register for.
Nothing is ever shown twice: the caption exists only at the width where its
columns are gone.

**A workspace with many sections gets a navigation column, not a row of tabs.**
The contest workspace has five sections today, and the architecture names two
more admin screens for the same contest — the query log (section 9.1) and the
reports (section 10). A horizontal row was already scrolling sideways at 375 px
with five, and a row has nowhere to put what a section still owes. The column
does: "Questions — 3" beside the link is a piece of work with an address, where
the same fact in a publish-gate report is a line the author has to translate
into a destination first.

It is **not** a panel, and that is the part worth not losing. There is no fill,
no border box and no shadow: the column is held by a single hairline in a 1 px
grid track — the same device the sign-in screen is built from, and the same one
this section means by "panels are separated by a rule". A `13rem` column, a 1 px
track, and the rest to the work. Below the mobile reset the grid collapses like
every other asymmetric one here: the column becomes a single scrolling row with
the same links in the same order, and the group labels are dropped rather than
stacked, because at that width they cost a line each and name two items.

**The sign-in screen splits at 1280 px, not at the mobile reset.** Between 760
and 1280 two columns technically fit and read badly: the heading column and the
form column come out the same width, the lede breaks after the third word, and
the page looks like two narrow strips instead of a composition. Below 1280 the
form goes under the heading, where it has the whole column.

### 5.1 The monitoring screens

Two screens let a contest's staff watch what its participants did
(architecture §9.4): the contest's own monitoring page and one page per
participant. Both live in the contest workspace and use the `compact`
vocabulary of section 5's density note: registers and feeds, mono labels,
tabular figures.

**The "Monitoring" item sits in the setup group, after the leaderboard, and
only for whoever may use it.** It is read while the contest runs and after,
like the leaderboard beside it. The item appears when `GET /contests/{id}`
says `may_monitor`, which is the server's own permission decision; the
screen never works out the rule for itself, so the item and the page cannot
disagree. Anybody else does not see the item, and the page answers them
with the 404 page, not a "forbidden" one.

**The contest page is a register beside a feed, not a dashboard.** The
participants table on the left, the live feed of the whole contest on the
right. The split follows the column, not the window (`@container`): side
by side (`minmax(0,1fr) 21rem`) only when the workspace column is at least
54rem wide, which it is at 1440 px; at 1024 and below the feed goes under
the table. There are no metric cards and no charts. The table already
holds the counts, and a card would repeat one of them in larger type.

- **The table scrolls inside its own box** (at most 42rem, or 70vh below
  the mobile reset) on both axes, with a sticky header and a sticky name
  column, so the page never scrolls sideways at 375 px. Every counter is a
  sortable column with `aria-sort`. The table opens sorted by name; a text
  column sorts ascending on its first press and a counter descending,
  because whoever sorts by a counter is looking for the largest. Filters
  above the table: flagged only, status, and a search over name and login.
- **Flags are badges, not colours on the row.** One warn-wash pill per
  raised flag. The label is `aria-hidden`, the explanation is `sr-only`
  text, and a `title` gives the same explanation to a pointer. A "?"
  tooltip above the table explains all six flags and says that browser
  signals are not proof. It sits outside the table's scroll box so the box
  edge cannot clip it. The flags are hints for a person to look into, and
  the screen presents them that way: nothing on it acts on a flag.
- **A row lights when its participant does something.** A new feed item
  washes the whole `<tr>` in `accent-wash` for 3 s. This is section 3's
  "what is happening now", which is what the accent is for. Rows are
  memoised, and a poll that changed nothing re-renders no row.
- **The feed reads down, newest at the bottom, and follows while you are
  at the bottom.** Rows are a fixed 3.5rem and windowed (with
  `aria-setsize`/`aria-posinset`), so a thousand lines keep a few dozen in
  the DOM. Scrolling up pauses the follow and keeps the reader's place;
  arrivals are counted in an "N new" button that jumps back. The list keeps
  at most 1000 lines. "Load older" pages back, and a list pushed off its
  newest end says so and offers "Back to the latest" rather than
  pretending to be live. Kind chips filter it: queries, answers, absences,
  pastes, network, sign-ins, tabs, clock. The server's own tab lines
  (`tab_created` and the rest) are set in muted ink, not faded: opacity
  would fail the contrast check.
- **Polling is quiet.** Every 5 s while the tab is visible, none while it
  is hidden, and one immediate poll on return. A 429 waits out its
  `Retry-After`, and other failures back off from 5 s to 60 s. The status
  line under the heading is a `role="status"` region that is always
  rendered, with only its text changing, so a screen reader hears a
  problem when it appears. The help text says the feed runs about two
  seconds behind by design.

**The participant page is one heading and five linkable tabs.** The heading
carries the name, login, status, clock start and finish, and the flag
badges. When the roster was truncated and holds no row for this
participant, the heading says nothing about flags rather than "none". It
also carries the CSV link and the way back to the contest page. The tabs are
links (`?tab=` in the URL, `aria-current` on the active one), so a tab can
be sent to a colleague. On a phone the strip scrolls sideways inside itself:
timeline, SQL queries, answers, workspace, sign-ins and networks.

- **Timeline:** the same feed component, without the participant's name on
  every line, plus a from/until range read in the contest's time zone.
- **SQL queries are a tab of their own**, because they are what the
  olympiad is about. Each row shows time, status, duration, rows, the
  address ("no address" for rows journalled before addresses were kept)
  and the error. Expanded, a row shows the full statement, read-only and
  highlighted with the editor's own colour tokens by a small lossless
  tokenizer rather than a second CodeMirror, which is an editor of about
  140 KiB. A copy button reports success or failure in a live region.
  Search waits 300 ms and stops at 200 characters. Rows use
  `content-visibility: auto`, because their heights vary.
- **Answers:** questions in order, attempts with value, verdict, points and
  time. Each attempt expands to the queries that led to it, and the text
  says so when the server kept only the first hundred.
- **Workspace:** the notes and tabs as they are now, and their history
  grouped by document (notes, open tabs in order, then closed tabs under
  their last title). Choosing a revision shows it as a line diff against
  the previous revision of the same document, with three lines of context
  and 400 rows at a time. Each changed line tells a screen reader "added,
  line n" or "removed, line n", not only a coloured sign. Notes are prose
  and are set in the text face even inside the `pre` that keeps their line
  breaks.
- **Sign-ins and networks:** sign-ins, sign-outs, failed sign-ins, address
  changes and parallel sessions, newest first, with every address seen
  listed once.

**Both pages hold the page-width rule of section 5.** They were checked at
1440, 1024, 768 and 375 px. Long SQL, IPv6 addresses, the tab strip, the
diff and the tables scroll inside their own boxes, and `scrollWidth` equals
`clientWidth` at every width.

**What a participant is told is part of this design, not a footnote.** The
play screen carries one line under its header, "The organiser sees your
queries, answers, notes and actions on this page", in the waiting room too.
The notes panel says "The organiser can see your notes." Both are set in small
secondary ink (`text-small`, `ink-2`) rather than a warning colour: they
state a fact, they do not raise an alarm.

### 5.2 The profile and report screens

`/profile` is the account's own screen; `/profile/contests/[contestId]`
is the report of one contest that has ended for its reader (architecture
§9.5). Both hold the same
"nothing in boxes, sections divided by a rule" language as the rest of the
product — the profile stopped being a list of account fields and became a
screen with real content once it gained a result to show.

**`/profile`** is a header, a one-line summary and a list, no tabs.

- **Header:** initials in a circle, name, and login and roles below it as
  tags — a wrapped line of labelled pairs replacing the stacked form the
  screen used to show — plus "Change password" and "Sign out". SPEC 10.4
  still applies here: initials, no photograph.
- **Summary:** four numbers in a row — contests, finished, queries run,
  questions solved — set mono with tabular figures, `grid-cols-4` collapsing
  to two below the narrow breakpoint. A failed read costs this section one
  line, not the page.
- **Contest list**, newest first, rows separated by a rule rather than cards.
  A finished contest's row shows its own result (points, or solved and
  penalty in ICPC) and links to its report; while the table is frozen the row
  says "Your place appears once the organiser opens the table" instead of a
  place, because the list never asks the leaderboard for one at all
  (architecture §9.5) — the report is where a place, when there is one,
  lives. A running contest's row offers only "Enter the contest" and nothing
  else: what is needed while a contest is on belongs to the contest's own
  screen. An upcoming row says when it starts. A long contest name wraps
  rather than widening the page.

**`/profile/contests/[contestId]`** opens only once the contest is over for
its reader; any other case — someone else's, still running, or nonexistent —
gets the same not-found page, because the profile does not say which of the
three it was. The header carries the way back to `/profile`, the contest's
name and when it ran, and a "Download CSV" of everything its reader ran in
it. Four tabs live in `?tab=` exactly as the monitoring participant page's
do — **Result** (default), **My queries**, **My answers**, **My notes** —
and the server reads the report plus only the one tab's data the address
names.

- **Result:** the reader's own points, or solved and penalty in ICPC; time
  worked; queries run and how many succeeded; then a table of every question
  — solved or not, attempts, time of the first correct answer, and points or
  ICPC penalty, the penalty read from the same cell the standings grid
  computes rather than derived again. Place and participant count appear only
  when the table is open; a frozen table says so instead, and an open table
  in winner mode says plainly that only the winner has a place, for the rows
  that are not it. A disqualified reader is told so, without the reason.
- **My queries, my answers, my notes:** the same rows, the same filters and
  the same layout an organiser's participant page shows for this reader
  (§5.1) — with no address column, no revision history, and no live refresh
  of running queries, because a report only opens after the contest has
  already ended for its reader.

**Both screens reuse rather than restyle the monitoring screens' own
components, not copies of them.** `components/product/tab-strip.tsx`
(`TabStrip({label, tabs, current})`) is the one strip both the monitoring
participant page and this report wear, each keeping its own addresses,
words and default tab. The query list (`query-row.tsx`, `query-log.tsx`,
`use-query-log.ts`), the answer list (`answer-attempts.tsx`) and the
read-only SQL block (`sql-block.tsx`, `sql-tokens.ts`) are the same
`components/product/` pieces §5.1 describes for the organiser's participant
page, with the words, the route and the address column passed in as props
rather than a second implementation for the second-person reading of them.

**The notes' two-column layout answers to the page's own narrow breakpoint**,
not to a `@container` query: this page's root does not declare one, so the
report's notes tab follows the same `narrow` breakpoint the summary strip and
everything else on the screen does.

**Widths 1440, 1024, 768 and 375 px hold on both screens**: the tab strip
scrolls sideways inside itself, the question table and the SQL blocks scroll
inside their own boxes, and a long contest name breaks rather than pushing
the page sideways.

**No avatars.** SPEC 10.4 stands: initials derived from the login, nowhere
in this profile.

## 6. Motion

Three durations: 120 ms for a reaction to input, 180 ms for a change of state,
260 ms for a layer arriving. Anything standing between an action and its result
fits inside 150 ms. There is one curve: `cubic-bezier(0.16, 1, 0.3, 1)`. A spring
(`stiffness 100, damping 20`) is used only when lists reorder in the admin area.

Perpetual animation is allowed where it carries information: the "contest is
running" indicator, the skeleton shimmer, the timer tick. On a screen where a
participant is solving a problem, nothing moves by itself.

`prefers-reduced-motion` is honoured at the token level: the durations collapse,
and `transform` transitions are replaced by `opacity` at ≤ 120 ms.

## 7. The state taxonomy

Nine states, one contract for every data container. A component that cannot
accept them is not accepted into the system. The product is a contest under a
timer with somebody else's database at the far end, so "empty", "broken" and "not
allowed" are not edge cases here but ordinary screens.

| State | What is shown | The distinction that matters |
|---|---|---|
| `idle` | the data | the ordinary state |
| `loading` | a skeleton shaped like the content to come | not a spinner; a circular indicator only inside a button |
| `empty` | "nothing here, and that is normal" + how data appears | there cannot be a clear-filter control |
| `empty-filtered` | "nothing matched" + clear the filter | differs from `empty` in principle; these two get confused most often |
| `error-recoverable` | cause + "try again" | network and 5xx; the only state with a retry |
| `error-terminal` | cause + a way out | 403/404; no retry, on purpose |
| `degraded` | a banner **above** the content that is shown | truncation at 1000 rows, a stale cache |
| `blocked` | cause + when it lifts | not started, finished, IP out of range, rate limit, disqualification |
| `provisioning` | progress + an estimate | our own; the game database is still being built |

The text of all nine is translatable strings from the frontend dictionary, not
whatever the server sent (section 8).

`blocked` also applies to an administrator's actions: the publish button is
blocked with the reason named and the missing work listed, rather than simply
going grey.

**The type is the contract.** `StateView` declares these as a discriminated
union, so `empty` cannot be given a clear-filter control and `empty-filtered`
cannot be rendered without one; `error-recoverable` requires a retry and
`error-terminal` refuses one. The confusion that used to be caught in review now
does not compile.

**Two of the nine are deliberately absent from `StateView`.** `loading` is drawn
from `Skeleton` by each container, because its appearance is a property of the
content it stands in for. `degraded` and `provisioning` arrive with the game
loop — a truncated result set and a game database still being built are steps 4
and 5 of the implementation order, and a state nothing renders is a state that
rots.

## 8. Multilingual text

Follows architecture section 6.2. The languages are `en`, `ro`, `ru`; the list
comes from a reference table, and a fourth is added as data without touching the
interface.

**Text splits into three layers with different owners:**

| Layer | Who translates | Source | Example |
|---|---|---|---|
| interface | the frontend | a dictionary in the repository | "Run", "Remaining" |
| authored content | the organiser | `*_translations` through the API | title, story, questions, choice labels |
| game data | **nobody** | the game database, always English | `guests.full_name = 'Margot Feilhaber'` |

**Error messages belong to the first layer.** The API returns a machine code
(`error.code`); the human text is assembled by the frontend from
`lib/i18n/errors.ts`. The server does not need to know the user's language in
order to report a failure.

**Nothing inside the result table is localised** — not names, not timestamps, not
numbers. A row is shown exactly as PostgreSQL returned it; otherwise a
participant cannot match what they see to what they wrote. Localisation lives in
the interface around the table, never inside it.

**String length is a variable.** Romanian is about a quarter longer than English.
No container is given a width fitted to a particular string. The language
switcher is the one exception — the codes are always two letters.

**The `lang` attribute** is set from the resolved language rather than nailed
into the markup.

## 9. Question format

Follows architecture section 6.1.

**Mode `multi`** — the console's right panel is a list of questions with their own
scores and attempts.

**Mode `single`** — a different screen, not a list of one: the panel stops being a
list, and the whole contest carries one question.

**A hidden question** (`questions.is_visible = false`) — the participant sees the
story and the answer field but not the question's text. That is a format, not a
blank, and the interface has to say so explicitly or it reads as a bug.

**A multiple-choice question** — labels are translated, identifiers (`choice_ids`)
are not. The identifier is what gets submitted, and it is shown in the interface
next to the label: that makes the language-independence of checking visible to
the author and to the participant alike.

**The publish gate is a screen.** A matrix of language × title / story / questions
with the missing translations, plus the "exactly one question when `single`"
check. The publish button is blocked with the reason named.

## 10. Images

The decision: a photograph is allowed where it does work, and forbidden where it
only fills space.

**It does work in exactly one place — above the crime story.** The picture sets
the scene before the participant goes into the database, and that is the one
surface of the product where atmosphere is part of the task rather than
decoration.

**There are no images in the contest listing.** It already has a number, a title,
a state and metrics; eight thumbnails would turn the register back into the wall
of cards this direction was chosen to get away from.

### 10.1 Rules for photographs

| Rule | Value |
|---|---|
| Licence | CC0 (preferred), public domain, CC BY with the attribution line filled in. `NC` and `ND` are not accepted |
| Storage | the file is downloaded and kept by us; hot-linking to somebody else's host is forbidden |
| Aspect | `16 / 9`, centre crop (`object-fit: cover`) |
| Processing | desaturation, contrast `1.06`, brightness `0.99` — set by the system, not by the author |
| Attribution | a field on the contest, rendered as a line under the picture; without it the image is not published — part of the publish gate |
| Loading | `loading="lazy"`, `decoding="async"`, explicit `width`/`height` against layout shift |

**Why 16:9 rather than cinematic 21:9.** Freely licensed archival material is
almost never wider than 3:2: these are scans of prints and negatives. A 21:9 rule
would force either cutting the subject or hunting for material that does not
exist in free collections. The rule adapts to what is actually available, not the
other way round.

**Why CC0 is preferred over public domain.** CC0 is a waiver of rights effective
worldwide. A work that entered the public domain under one country's law (a work
of the United States government, say) has no guaranteed status in other
jurisdictions; Wikimedia prints that caveat directly under such files. The
demonstration picture in `preview.html` is exactly that case, and it is recorded
in `CREDITS.md`.

### 10.2 The title over the picture

The text does not sit on the photograph but on a scrim that resolves to the page
ground: `--color-scrim-a` (97 % of the ground) → `--color-scrim-b` (72 %) →
transparent at 76 % of the height. The tokens differ per theme, so the scrim is
white in the light theme and almost black in the dark one, and the title's
contrast does not depend on what happens to be in the bottom third of somebody
else's picture.

This is not cosmetics: the organiser picks the subject, and the system is obliged
to guarantee legibility whatever they pick.

### 10.3 When there is no picture

The header becomes a hatched plate of the same aspect with the same typography —
not one shift in the layout. A contest without an image looks deliberate rather
than unfinished.

### 10.4 Avatars

Uploading participants' photographs is not added. Initials derived from the
identifier solve the same problem and create no personal data that would have to
be stored, moderated and deleted on request.

## 11. Component inventory

Three circles. The component layer is `shadcn/ui`, copied into the repository and
redrawn completely against the tokens: `Base UI` underneath covers accessibility,
and copying removes the risk of somebody else's major release landing in the
middle of a contest. The default `shadcn` look may not be used — radii, palette
and density are overridden wholesale.

**Circle 1, primitives (~26):** Button (4 variants × 4 sizes), IconButton, Input,
Textarea, Select, Combobox, Checkbox, Radio, Switch, Field (label + control +
hint + error as one block), Badge, Tag, Avatar, Tooltip, Dialog, Sheet,
DropdownMenu, Toast, Tabs, Separator, Skeleton, Spinner, Kbd, Link, Progress,
Pagination.

**Circle 2, product components (~15):** DataTable (virtualisation, sticky head,
column types, explicit `NULL`), ResultGrid (a specialisation for query results,
CSV, the truncation banner), CodeEditor (CodeMirror 6, our theme, highlighting
the error position PostgreSQL returns), SchemaTree, ERDiagram, Timer,
ContestRegister, QuestionCard, ChoiceList, AnswerField, LeaderboardRow, StateView
(the implementation of section 7), FilterBar, ExportMenu, LanguageSwitcher,
PublishGate, AuditRow.

**Circle 3, layouts:** PublicShell, AdminShell, StudentShell, ConsoleShell (three
panes with resizable, remembered sizes). Plus `Band` and `AppBar`, which every
shell is built from.

**Built so far:** Button, Input, Label, Field, Tag, Skeleton, StateView, Band,
AppBar, Mark, PublicShell, AdminShell, ContestRegister, LanguageSwitcher,
ThemeToggle, ExportMenu.

### 11.1 ExportMenu

The decision: a download is a link, not a button that fetches.

**What leaves the server is decided by the server.** `ExportMenu` renders one
anchor per format, pointed at the endpoint that produces it, with `download`
set. It fetches nothing, holds nothing and serialises nothing. That is not
minimalism: a control that built the file in the page could only ever offer
what the page had already been given, and on both surfaces that is the wrong
amount. The participant's log panel holds one page of fifty rows out of a
session that can run to hundreds — a "download" handing over the fifty would be
the wrong answer in the format that looks most authoritative. The contest
package is worse: it carries the reference answers, which are never sent to a
page at all, so there is nothing in the browser to assemble it from and there
must not be.

**It is called a menu because it is meant to grow into one.** Architecture
§9.1 promises NDJSON and XLSX beside CSV on the administrator's journal panel,
which is not built. Today each surface offers exactly one format, so each
renders a list of one — not a disclosure widget concealing a single item, which
is a click charged for nothing.

**Four surfaces carry it.** On the contest overview, one JSON link: the whole
contest as a file to author again next year. On the participant's query-log
panel, one CSV link: their own session as a file, offered only once there is a
row in it. On the contest's monitoring page and on a participant's monitoring
page (section 5.1), one CSV link each: the whole feed of the contest or of that
participant, streamed and audited by the server. Nothing else does; a screen that has no data worth taking away does
not get an empty group heading, because a screen reader announces one all the
same.

**The format name is not translated.** "CSV" and "JSON" are proper nouns in all
three languages, and the extension on the saved file says the same word again.
What is translated is the accessible name, which says *what* is being
downloaded rather than only how — the two links would otherwise both read
"download" to somebody who reaches the page through them.

**Nothing about the file's name is decided here.** `download` is a suggestion;
the server's `Content-Disposition` is what a browser actually obeys, and both
endpoints set one. Deciding it twice is how the two answers start disagreeing.

## 12. Directory structure

```
frontend/
├── app/                   routes; a folder is a URL
│   ├── layout.tsx           <html>, fonts, lang, data-theme, dictionary
│   ├── error.tsx            the last boundary before the framework's own
│   ├── not-found.tsx        404
│   ├── global-error.tsx     the failure that took the layout with it
│   ├── (public)/            no session: the group's layout is the shell
│   │   └── login/             page, its form, its Server Action
│   └── (admin)/             behind one: the group's layout is the shell
│       └── contests/          page, loading, error, its register
├── components/
│   ├── ui/                  primitives with no domain knowledge
│   ├── product/             domain components more than one route uses
│   └── layout/              the app frame: Band, AppBar, shells, switchers
├── lib/
│   ├── api/                 transport, wire schemas, the API's address
│   ├── auth/                session, guards, sign-in, where an account lands
│   ├── i18n/                dictionaries, locale resolution, error messages
│   ├── theme/               the theme cookie and its attribute
│   ├── format/              dates and numbers by locale
│   └── utils.ts             cn(), which also teaches tailwind-merge the scale
├── scripts/                 the checks CI runs
└── styles/tokens.css        the single source of every token
```

**A parenthesised folder is a group, not a segment.** `(public)` and `(admin)`
appear in no URL; the addresses are `/login` and `/contests`. What a group
carries is the layout, so the shell is put on once for a set of screens rather
than by each page for itself — which is how sign-in and the constructor came to
wear theirs in two different places. A new administrative screen is a folder
inside `(admin)` and arrives already framed.

**A route owns what only it uses.** Its page, the components that page renders
and the Server Actions it submits to live in the route folder. A component a
second route reaches for is promoted to `components/product/`, and the move is
the moment it stops being one route's business.

Next documents three ways to organise an application — everything outside `app`,
everything in shared folders at its root, or split by feature and route — and
asks only that one be chosen and followed. This is the third.

The alternative — every component in `components/` regardless — reads tidier in
a listing and worse in practice: it separates a form from the action it posts
to, and it forces a component that will only ever serve one screen to be named
as though it serves the product. The rule as written is also the one Next
documents, and it needs no judgement at the moment of writing a file: one route,
keep it; two, move it.

`components/ui` never mentions the domain. A primitive that knows what a contest
is belongs one directory over.

**Imports point one way:** `app` → `components` → `lib`. Nothing in `lib`
imports a component. Within `lib`, `api` is the lowest layer; `api/server.ts` is
the single deliberate crossing, because an authenticated request needs both the
transport and the session.

**A test sits beside its source.** `foo.tsx` is tested by `foo.test.tsx`. A
mirrored `__tests__` tree has to be kept in step by hand, and renaming a file
becomes a two-file operation whose second half is easy to forget. Vitest and the
Next testing guide both colocate by default. End-to-end tests are the exception
and get their own directory when they arrive: they belong to a journey rather
than to a file.

**The `/design` showcase is not Storybook.** A separate tool means a separate
build and inevitable drift between the showcase and the product. The showcase
lives on the same Next.js and is assembled from the same code; it shows every
applicable state from section 7 and the theme, density and language switches.

**The showcase is deferred.** It was built and then removed: to show the register
it needs contests, and invented data does not belong in the repository. It
returns when the constructor (architecture step 3) can give it real ones. Until
then the system is held not by a showcase but by the checks in CI: the ESLint
rule against arbitrary colours, sizes and weight 700; the contrast script over
the tokens; the check that every error code the API returns has a message; and
the `cn()` tests on the scale.

## 13. What was fixed in the existing skeleton

The skeleton was created by `create-next-app` (commit `bdbcc31`). What matched:
Next.js 16.3.2 App Router, Tailwind v4 on `@tailwindcss/postcss`, TypeScript, a
structure without `src/`. Three defects of the starter were fixed before anything
else, and all three are now done:

1. **`subsets: ["latin"]`** on the loaded fonts silently drops Cyrillic and
   `latin-ext` — with three languages, Romanian and Russian would have rendered
   from a fallback face.
2. **`body { font-family: Arial, Helvetica, sans-serif }`** in `globals.css` sat
   after `@theme` and killed the font variables.
3. **`lang="en"`** was nailed into `app/layout.tsx` with three languages in play.

Plus the starter `metadata` ("Create Next App").

**`frontend/AGENTS.md` requires** reading the guide in
`node_modules/next/dist/docs/` before writing code: this version carries breaking
changes relative to the Next.js one remembers. That is a requirement, not a
suggestion.

## 14. Accessibility and checks

Contrast ≥ 4.5:1 for text and ≥ 3:1 for control boundaries in both themes —
**checked by a script over the tokens in CI** (`frontend/scripts/contrast.mjs`),
not by eye. On draft values it caught three failures that were invisible: muted
text at 2.4:1 instead of 4.5:1 and an input border at 1.49:1 instead of 3:1.

One exception is recorded explicitly: the decorative annotation caption
(`--color-ann`, ~1.7:1) does not pass the threshold and has no right to be the
only carrier of information. Where a caption carries data (the size scale, token
values) it is set in `--color-ink-3` (5.5:1).

Focus is always visible: a 2 px ring in the accent at 2 px offset, declared once
globally so a control added tomorrow arrives with it rather than without;
`outline: none` without a replacement is forbidden. The console is fully
keyboard-driven: `Cmd/Ctrl+Enter` runs, `Esc` aborts, `Cmd/Ctrl+K` opens schema
search. The result table is a real `<table>` with `scope`, not a grid of `div`s.

**A form-level failure is announced, not just displayed.** The sign-in error is
written once, because the API answers `invalid_credentials` without saying which
field was wrong — deliberately, so the form cannot be used to enumerate logins —
and both fields carry `aria-invalid` and point at that one message. Showing it on
screen and saying nothing to a screen reader is the same bug as not showing it at
all.

**The surfaces the page never drew still belong to it.** Selection, the caret,
the scrollbar, the focus ring, underline offset and the figures in tabular data
all ship with browser defaults that belong to no design system. They are themed
from the palette in `globals.css`. This is the cheapest signal that an interface
was built rather than assembled, and the one most reliably skipped.

## 15. Boundaries

No emoji. No stock photography: images are free-licensed only and only under the
rules of section 10 — on the case page and the public landing, but not in
listings, not in metric cards and not in empty states. No glows and no gradient
text. No purple. No `h-screen` — only `min-h-[100dvh]`. No `bg-[#hex]`. No
marketing row of three identical feature cards (the grid of nine state
demonstrations in section 7 is a different thing: it shows an inventory, it does
not advertise three advantages). No width fitted to a string. There is no weight
700 in the system.

## 16. Order of work

1. ~~Fix the skeleton (section 13); load the fonts with the `latin`, `latin-ext`
   and `cyrillic` subsets.~~ **Done.**
2. ~~The token layer in `styles/tokens.css`, the ESLint rule against arbitrary
   colours, the contrast script in CI.~~ **Done**, plus the type scale, the
   motion tokens, the density modes and the named radii; Tailwind's stock scales
   (`--text-*`, `--radius-*`, `--tracking-*`) are erased.
3. ~~Primitives over the headless layer, redrawn against the tokens; the
   `/design` showcase.~~ **Done** except the showcase, which is deferred
   (section 12).
4. ~~The vertical slice: `/login` and `/contests` against the live backend — the
   form with a server error, the session over a single origin, four states, both
   shells, both themes, both density modes, three languages.~~ **Done.**

Next is item 3 of the architecture: content CRUD and the constructor, which by
now is assembled from ready parts.

## 17. Side edit to the architecture

`docs/ARCHITECTURE.md` does not describe the frontend architecture: section 2.1
lists the screens and section 13 gives the frontend one line. A separate change
adds a section covering the Server/Client Component boundary, the session path (a
single origin through Caddy — otherwise a `Secure`+`SameSite` cookie never
arrives), the TanStack Query invalidation strategy, the SSE approach for the
timer and monitoring, and a reference to the state taxonomy.

## 18. Mockups

The mockup of the accepted direction sits next to this file:
[`preview.html`](preview.html). It opens in a browser as it is, follows the
operating system's theme, and is reproducible from this specification — the
specification is the source of truth, the mockup is an illustration.

It predates the revision of 2026-08-26: the column width and the type scale in it
are the original values. The reasoning it illustrates still holds; the numbers in
this document are the ones that ship.

The same page is published as a private artifact:
<https://claude.ai/code/artifact/08d4a86a-de62-4018-973a-c3e9aa23042d>

The rejected options are kept so the decision can be revisited knowing the
alternatives:

- *Kartoteka* — a register, zero rounding, inverted ink instead of coloured
  buttons: <https://claude.ai/code/artifact/a2fcf640-b277-44f7-9728-d5faf845a000>
- The first option — warm neutrals, plates, a full treatment of every section:
  <https://claude.ai/code/artifact/b656b2bc-0d87-40ec-80aa-1b37f14ddd06>

The artifacts are private and reachable only by the account that owns them.
