# Play screen measurements

How the participant's play screen performs in a real browser, measured on
`main` at commit `83e0935` (2026-10-27) ahead of the olympiad on 2 November
2026. The pre-olympiad review (`pre-olympiad-review-2026-10-26.md`) read the
play screen's code and found nothing in the editor itself; this measures it.
Every area is inside its threshold, on a CPU slowed four times as well as at
full speed; the one close to its line is the throttled first load's total
blocking time. Nothing here calls for a change before the olympiad.

`make e2e-measure` repeats it against any stack the smoke test can use
(`frontend/e2e/play-measure.perf.ts`, `frontend/playwright.perf.config.ts`).

## Verdicts

| Area | Threshold | Unthrottled | CPU 4× | Verdict |
|---|---|---|---|---|
| Typing, short document | interaction p95 < 200 ms; no long task > 200 ms | p95 < 16 ms, max 24 ms, no long task | p95 16 ms, max 16 ms, no long task | Good |
| Typing, middle of a 500-line document | the same | p95 16 ms, max 32 ms, no long task | p95 32 ms, max 64 ms, no long task | Good |
| Running a query | press < 200 ms; no long task > 200 ms | press 48 ms, answer drawn in 54 ms (median), no long task | press 64 ms, answer drawn in 60 ms, no long task | Good |
| A large result rendering (1,000 rows) | no long task > 200 ms | none; drawn in 85 ms | none; drawn in 137 ms | Good |
| The heaviest result the budget admits (1,000 rows, ~4 MB) | no long task > 200 ms | none; drawn in 131 ms | one of 54 ms; drawn in 268 ms | Good |
| Scrolling those results | no long task; no frame over 50 ms | none; every frame 16.7 ms | none; every frame ≤ 16.8 ms | Good |
| Switching SQL tabs (a short one and a 500-line one) | interaction p95 < 200 ms | p95 24 ms, no long task | p95 32 ms, no long task | Good |
| The side panel: hiding, showing, its tabs | interaction p95 < 200 ms | p95 48 ms, no long task | p95 64 ms, no long task | Good, unless the single 720 ms press from a trial run reproduces; it did not recur in 54 logged presses or the final runs. If in doubt, re-measure on a quiet host before the olympiad |
| First load: LCP | LCP ≤ 2.5 s | 96 ms | 220 ms | Good, but it is the page's first paint, not its content (below) |
| First load: editor ready (content ready) | none set; for the reader | 233 ms | 716 ms | — |
| First load: TBT | TBT ≤ 200 ms | 0 ms | median 123 ms, worst 181 ms, close to the line | Good |

Each figure is the median of three runs; the spread is in the tables below.
200 ms is web.dev's line between a good and a poor interaction (INP), and
2.5 s and 200 ms are the "good" lines for LCP and TBT. A single long task
over 200 ms while typing would have been called a defect; there was no long
task while typing at all. The worst interaction anywhere was 64 ms. The
longest task was 110 ms, in the throttled first load; elsewhere the longest
was 58 ms, the heaviest result rendering at 4×.

LCP is not "the content in 220 ms". In four of the six first loads LCP fell
at the same instant as the first contentful paint, so it is the first
thing painted, the page's shell, rather than the editor or a question; in the other two it came later (404 ms and 480 ms). The element
was not recorded in these runs; the script now records it (`lcpElement` in
the output). When the content a participant works in is there is the
separate "editor ready" figure: 233 ms, and 716 ms at 4×.

## What this does not measure

- **Forty participants at once, on the real host.** This is one browser
  against a stack on the same machine. The start-of-contest burst (forty
  page renders, the parallel reads, the events channel, the autosave and the
  queries on one core-database pool) is the review's P5 and needs the real
  host and many browsers; nothing here speaks to it.
- **The network.** The stack is on loopback. First load here is CPU and
  parsing, not transfer; a participant's LCP adds their network's round trips
  and the bytes of the play route.
- **A real weak laptop.** "4×" is Chromium's CPU throttle on an Apple M1 Pro,
  which slows the page's main thread, not the GPU, the compositor or the
  disk. It stands in for a weak laptop; it is not one. The calibration
  figure in each run shows the throttle took: a fixed loop ran in about
  90 ms at full speed and about 320 ms throttled, 3.5 times slower.
- **Headless rendering.** Chromium ran headless (the full browser in its new
  headless mode, version 153). Paint and composite may not cost what they
  cost on a screen, and frames come at a steady 60 Hz.
- **A large schema.** The seeded contest has one table, so completion had
  little to offer. A contest with dozens of tables and columns gives the
  completion source more to filter on each keystroke; the completion chunk
  is the one script that appeared at all while typing (36 ms of timer work
  over a whole throttled typing run).
- **Several thousand rows.** No more than 1,000 rows ever reach the browser:
  the Query Runner cuts an answer at `QUERY_MAX_ROWS` (1000) and
  `QUERY_MAX_BYTES` (5 MiB), the values the deployment sets in
  `deploy/docker-compose.yml`. The large-result query asks for 5,000 and is
  cut to 1,000, as a careless participant's would be. Raising the cap would
  change what this measures.

## Method

**The stack.** `make e2e-stack-up` under a project name, port and subnet of
its own: the deployment's compose file with images built from this tree. The
interface is the production build (`npm run build` in the image, served with
`NODE_ENV=production`; hashed chunks, no development client), which is the
only build whose numbers mean anything. The script also fails a run if the
page asks for `/_next/webpack-hmr`, the development server's hot-reload
channel, so it cannot measure a development server by mistake. The smoke test's seed made the
accounts and the running `read_write` contest (one table, `suspects`), and its
teardown closed them.

**The browser.** Playwright 1.63 driving Chromium 153 headless, at
1920×1080, English locale. Each run had a fresh context (an empty cache) with
the participant's session, so every run starts as a participant opening the
page. The CPU throttle is CDP's `Emulation.setCPUThrottlingRate` at rate 4,
set before the first navigation.

**Repeats and noise.** Every scenario ran three times under each condition,
the conditions alternating so that drift fell on both alike; the figures are
the medians, with the minimum and maximum. Before each run the script waited
for the machine's one-minute load average to fall to 0.6 per core (6 on these
10 cores) and recorded it: every run below started between 3.2 and 6.0. The
machine is shared, and its load spiked to 50 twice during the session; the
script waited those out rather than measure through them. The load is read
again when each run ends, and a run that ended above the ceiling is flagged
as noisy in the output; that was added after these runs, so their end load
is "-" in the table below.

**Keystroke latency.** A `PerformanceObserver` on `event` entries with
`durationThreshold: 16`, from before the page's own script runs. The
keydown, keypress, input and keyup entries are grouped by `interactionId`,
and an interaction's latency is the longest of them: INP's own definition,
from the key's timestamp to the next paint. 252 characters of SQL were typed
in each run (comments, a grouped query, a second statement), one key at a
time with 80 ms between them, about twelve a second. The browser does not
report an interaction under 16 ms, so one that left no entry counts as zero
and the tables show "<16". "Handler" is the time inside event handlers, of
the interactions the browser reported. The 500-line document is generated
SQL (commented CTEs, joins, window functions), put into the editor whole, as
a paste would, with the caret on line 250; the typing happens there.

**Long tasks.** A `longtask` observer (over 50 ms), filtered to each
scenario's window: from just before the first action to the end of a second
with no long task. A `long-animation-frame` observer was kept beside it, for
which scripts a slow frame ran.

**Queries.** The Run button is pressed and the window ends when the answer is
drawn (the button is disabled from the press until the answer is committed)
and the page has been quiet for a second. "Press" is the Event Timing latency
of the click; "drawn" is the wall time from the press to the answer on screen,
round trip included. It is taken in the test process, from the press until
Playwright sees the Run button enabled again, so it is an upper bound that
includes the DevTools protocol's and the polling's latency, not a timestamp
from inside the page. Five runs of a three-row query per run. The large result
is `generate_series(1, 5000)` with six columns (an id, an md5, a name, a
timestamp, a boolean, a 96-character note), cut to 1,000 rows. The heaviest
result is 1,000 rows of eleven columns, ten of them 384 characters: about
4 MB against the 5 MiB budget, every cell under the table's 2,000-character
clip, so all of it is laid out when it scrolls into view.

**Scrolling.** Sixty wheel turns of 400 px down the result table and thirty
back up, 40 ms apart, with a `requestAnimationFrame` loop timing each frame.
The table keeps only the rows in view in the DOM (2.75 rem rows, eight rows
of overscan), so scrolling is what makes it render; the run checks that rows
from the 300s onward appeared.

**Tab switches and the side panel.** Ten switches between two SQL tabs, one
with a short query and one with the 500-line document. For the side panel,
four rounds of hiding and showing it with the header's "Questions panel"
toggle, then each of its tabs (Story, Questions, Organisers, Notes, Table)
twice. Eighteen presses per run.

**First load.** FCP from the `paint` entry, LCP from the last
`largest-contentful-paint` entry, and the editor's arrival (CodeMirror
replacing the plain textarea it starts as). TBT is Lighthouse's definition:
the sum of each long task's time over 50 ms after FCP, up to the end of the
last long task before five seconds without one, which stands in for TTI.
Lighthouse's TTI also waits for the network to be quiet; this does not.

## Results

Median [minimum–maximum] over three runs. Times in milliseconds. Event Timing
reports durations in steps of 8 ms.

### Typing (252 keystrokes per run)

| Document | Condition | Reported ≥ 16 ms | p50 | p95 | Max | Handler max | Long tasks |
|---|---|---|---|---|---|---|---|
| Short | unthrottled | 9 [7–15] | <16 | <16 [<16–16] | 24 [24–24] | 4 [3–4] | 0 |
| Short | 4× | 16 [10–18] | <16 | 16 [<16–16] | 16 [16–16] | 9 [8–10] | 0 |
| 500 lines | unthrottled | 117 [109–130] | <16 [<16–16] | 16 [16–16] | 32 [24–32] | 14 [14–17] | 0 |
| 500 lines | 4× | 252 [252–252] | 24 [24–24] | 32 [32–32] | 64 [56–64] | 31 [28–33] | 0 |

No keystroke in any run reached 200 ms, and none produced a long task.

### Running a query (five runs of a three-row query, per run)

| Condition | Press, max | Round trip, p50 | Drawn, p50 | Drawn, max | Long tasks |
|---|---|---|---|---|---|
| unthrottled | 48 [48–48] | 51 [48–52] | 54 [52–55] | 89 [88–91] | 0 |
| 4× | 64 [56–72] | 51 [46–51] | 60 [56–60] | 142 [134–154] | 0 |

### Large results and scrolling

| Result | Condition | Press | Drawn | Render long tasks | Longest | Scroll long tasks | Frame p95 | Frames > 50 ms |
|---|---|---|---|---|---|---|---|---|
| 1,000 rows, 6 columns | unthrottled | 48 [48–48] | 85 [84–86] | 0 | — | 0 | 16.7 | 0 of 271 |
| 1,000 rows, 6 columns | 4× | 56 [48–64] | 137 [126–145] | 0 | — | 0 | 16.7 | 0 of 272 |
| 1,000 rows, 11 columns, ~4 MB | unthrottled | 48 [40–56] | 131 [120–134] | 0 | — | 0 | 16.7 | 0 of 271 |
| 1,000 rows, 11 columns, ~4 MB | 4× | 64 [56–72] | 268 [262–277] | 1 [1–1] | 54 [54–58] | 0 | 16.8 | 0 of 275 |

The one long task, on the heaviest result throttled, is React's scheduler
committing the table (attributed to the React DOM chunk's `MessagePort`
handler), 4 to 8 ms over the 50 ms line.

### SQL tabs (ten switches per run)

| Condition | p50 | p95 | Max | Handler max | Long tasks |
|---|---|---|---|---|---|
| unthrottled | 16 [16–16] | 24 [16–24] | 24 [16–24] | 4 [4–4] | 0 |
| 4× | 24 [24–24] | 32 [32–40] | 32 [32–40] | 8 [8–8] | 0 |

### Side panel (eighteen presses per run)

| Condition | p50 | p95 | Max | Handler max | Long tasks |
|---|---|---|---|---|---|
| unthrottled | 16 [16–16] | 48 [40–48] | 48 [40–48] | 9 [9–12] | 0 |
| 4× | 24 [16–24] | 64 [56–64] | 64 [56–64] | 15 [15–17] | 0 |

### First load

| Condition | FCP | LCP | Editor ready | TBT | Long tasks | Longest |
|---|---|---|---|---|---|---|
| unthrottled | 92 [72–96] | 96 [92–404] | 233 [202–456] | 0 [0–0] | 0 | — |
| 4× | 212 [132–220] | 220 [212–480] | 716 [709–721] | 123 [82–181] | 5 [5–5] | 110 [89–110] |

The throttled load's five long tasks are evaluating the framework's chunk
and hydrating (React's scheduler, up to 110 ms) and evaluating the CodeMirror
chunk (up to 86 ms), which is imported after the page is up so that it stays
off the critical path (`components/product/code-editor-core.ts`). Throttled,
the editor is usable about half a second after the first paint.

## Surprises

- **The 500-line document costs more than the short one, but little.** Every
  throttled keystroke in it was reported (each took at least 16 ms, against
  about one in sixteen in the short document), and the handlers' time roughly
  tripled. The worst keystroke was still 64 ms, a third of the threshold.
- **A 720 ms side-panel interaction appeared once, in a trial before these
  runs**, unthrottled, with 6 ms in handlers and no long task: the time was
  spent waiting for a frame, not in the page's code. It did not recur in 54
  further presses logged one by one, nor in the six runs above, and it
  coincided with the host's load spikes. It is reported for completeness;
  the medians are what the verdicts rest on.
- **The large answers cost less than expected.** The table's windowing does
  its job: a thousand rows render the same few dozen rows as three do, and
  scrolling them never dropped a frame even at 4×. What grows with the
  answer is parsing it, which reached a single 54 ms task only for a 4 MB
  answer on a throttled CPU.

## Raw runs

Every run, in order, with the one-minute load average it started at and ended
at ("-": not recorded in these runs). "cal" is the calibration loop's time.

| Scenario | Condition | # | Load at start | Load at end | Figures |
|---|---|---|---|---|---|
| first load | unthrottled | 1 | 5.86 | - | FCP 96, LCP 96, editor 233, TBT 0, long tasks 0, cal 90 |
| first load | 4× | 1 | 5.63 | - | FCP 132, LCP 480, editor 721, TBT 181, long tasks 5, longest 110, cal 318 |
| first load | unthrottled | 2 | 5.34 | - | FCP 92, LCP 92, editor 202, TBT 0, long tasks 0, cal 97 |
| first load | 4× | 2 | 5.47 | - | FCP 212, LCP 212, editor 716, TBT 82, long tasks 5, longest 89, cal 318 |
| first load | unthrottled | 3 | 4.94 | - | FCP 72, LCP 404, editor 456, TBT 0, long tasks 0, cal 85 |
| first load | 4× | 3 | 5.02 | - | FCP 220, LCP 220, editor 709, TBT 123, long tasks 5, longest 110, cal 319 |
| typing, short | unthrottled | 1 | 4.70 | - | reported 15, p95 16, max 24, handler 4, long tasks 0, cal 92 |
| typing, short | 4× | 1 | 4.04 | - | reported 16, p95 16, max 16, handler 9, long tasks 0, cal 324 |
| typing, short | unthrottled | 2 | 3.46 | - | reported 9, p95 <16, max 24, handler 4, long tasks 0, cal 94 |
| typing, short | 4× | 2 | 3.31 | - | reported 10, p95 <16, max 16, handler 8, long tasks 0, cal 318 |
| typing, short | unthrottled | 3 | 3.29 | - | reported 7, p95 <16, max 24, handler 3, long tasks 0, cal 93 |
| typing, short | 4× | 3 | 3.65 | - | reported 18, p95 16, max 16, handler 10, long tasks 0, cal 322 |
| typing, 500 lines | unthrottled | 1 | 3.53 | - | reported 130, p50 16, p95 16, max 32, handler 17, long tasks 0, cal 87 |
| typing, 500 lines | 4× | 1 | 3.79 | - | reported 252, p50 24, p95 32, max 56, handler 28, long tasks 0, cal 319 |
| typing, 500 lines | unthrottled | 2 | 3.21 | - | reported 109, p95 16, max 24, handler 14, long tasks 0, cal 88 |
| typing, 500 lines | 4× | 2 | 4.12 | - | reported 252, p50 24, p95 32, max 64, handler 31, long tasks 0, cal 317 |
| typing, 500 lines | unthrottled | 3 | 5.84 | - | reported 117, p95 16, max 32, handler 14, long tasks 0, cal 87 |
| typing, 500 lines | 4× | 3 | 5.12 | - | reported 252, p50 24, p95 32, max 64, handler 33, long tasks 0, cal 319 |
| run | unthrottled | 1 | 4.85 | - | press 48, round trip 52, drawn 55 (max 89), long tasks 0, cal 90 |
| run | 4× | 1 | 5.43 | - | press 72, round trip 51, drawn 60 (max 154), long tasks 0, cal 323 |
| run | unthrottled | 2 | 5.74 | - | press 48, round trip 51, drawn 54 (max 88), long tasks 0, cal 79 |
| run | 4× | 2 | 5.76 | - | press 56, round trip 51, drawn 60 (max 134), long tasks 0, cal 318 |
| run | unthrottled | 3 | 5.56 | - | press 48, round trip 48, drawn 52 (max 91), long tasks 0, cal 94 |
| run | 4× | 3 | 5.75 | - | press 64, round trip 46, drawn 56 (max 142), long tasks 0, cal 322 |
| large result | unthrottled | 1 | 5.72 | - | press 48, drawn 86, long tasks 0 / 0, frame p95 16.7, cal 99 |
| large result | 4× | 1 | 5.45 | - | press 64, drawn 145, long tasks 0 / 0, frame p95 16.7, cal 325 |
| large result | unthrottled | 2 | 5.00 | - | press 48, drawn 84, long tasks 0 / 0, frame p95 16.7, cal 96 |
| large result | 4× | 2 | 5.01 | - | press 48, drawn 126, long tasks 0 / 0, frame p95 16.7, cal 322 |
| large result | unthrottled | 3 | 5.92 | - | press 48, drawn 85, long tasks 0 / 0, frame p95 16.7, cal 82 |
| large result | 4× | 3 | 5.53 | - | press 56, drawn 137, long tasks 0 / 0, frame p95 16.8, cal 317 |
| heaviest result | unthrottled | 1 | 5.55 | - | press 48, drawn 131, long tasks 0 / 0, frame p95 16.7, cal 83 |
| heaviest result | 4× | 1 | 6.00 | - | press 56, drawn 277, long tasks 1 (58) / 0, frame p95 16.7, cal 325 |
| heaviest result | unthrottled | 2 | 5.47 | - | press 56, drawn 134, long tasks 0 / 0, frame p95 16.7, cal 100 |
| heaviest result | 4× | 2 | 5.63 | - | press 64, drawn 268, long tasks 1 (54) / 0, frame p95 16.8, cal 322 |
| heaviest result | unthrottled | 3 | 5.15 | - | press 40, drawn 120, long tasks 0 / 0, frame p95 16.7, cal 80 |
| heaviest result | 4× | 3 | 5.36 | - | press 72, drawn 262, long tasks 1 (54) / 0, frame p95 16.8, cal 322 |
| tab switch | unthrottled | 1 | 5.16 | - | p50 16, p95 16, max 16, handler 4, long tasks 0, cal 82 |
| tab switch | 4× | 1 | 5.41 | - | p50 24, p95 32, max 32, handler 8, long tasks 0, cal 317 |
| tab switch | unthrottled | 2 | 5.93 | - | p50 16, p95 24, max 24, handler 4, long tasks 0, cal 91 |
| tab switch | 4× | 2 | 5.57 | - | p50 24, p95 32, max 32, handler 8, long tasks 0, cal 317 |
| tab switch | unthrottled | 3 | 5.28 | - | p50 16, p95 24, max 24, handler 4, long tasks 0, cal 97 |
| tab switch | 4× | 3 | 4.93 | - | p50 24, p95 40, max 40, handler 8, long tasks 0, cal 316 |
| side panel | unthrottled | 1 | 4.48 | - | p50 16, p95 40, max 40, handler 12, long tasks 0, cal 88 |
| side panel | 4× | 1 | 4.25 | - | p50 24, p95 64, max 64, handler 17, long tasks 0, cal 317 |
| side panel | unthrottled | 2 | 3.90 | - | p50 16, p95 48, max 48, handler 9, long tasks 0, cal 96 |
| side panel | 4× | 2 | 3.83 | - | p50 16, p95 64, max 64, handler 15, long tasks 0, cal 318 |
| side panel | unthrottled | 3 | 3.55 | - | p50 16, p95 48, max 48, handler 9, long tasks 0, cal 82 |
| side panel | 4× | 3 | 3.69 | - | p50 24, p95 56, max 56, handler 15, long tasks 0, cal 319 |

For the result rows, "long tasks a / b" is while rendering / while scrolling.

## Running it again

```sh
make e2e-stack-up
make e2e-measure E2E_ENV_FILE=<stack dir>/smoke.env
make e2e-stack-down
```

It takes about fifteen minutes, longer while it waits for the load to drop. The numbers go to
`frontend/test-results/play-measurements.json` (or `E2E_MEASURE_OUT`), each
run with its load average at the start and the end (a run that ended above
the ceiling is marked noisy), its LCP element on first load, and the scripts
its slow frames ran, and the
medians are printed at the end. `E2E_MEASURE_TRACE=<dir>` records a Chromium
trace of each scenario's first run, for finding where a slow one spent its
time; a traced run is left out of the medians. A test fails only when its
measurement did not happen; the thresholds are for the reader.
