import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";

import { expect, test, type Browser, type Page } from "@playwright/test";

import { readSeed, STATE_DIR } from "./api";
import { newSqlTab, signIn } from "./support";

/**
 * How the participant's play screen performs in a real Chromium, on the
 * smoke test's seeded contest: keystroke latency in the editor, long tasks
 * while a query runs, a large result renders and scrolls, SQL tabs switch and
 * the side panel opens, and LCP and total blocking time on first load
 * (docs/research/play-screen-measurements-2026-10-27.md). Each scenario runs
 * unthrottled and with the CPU slowed 4×, three times each, alternating, and
 * the numbers go to E2E_MEASURE_OUT. Run with `make e2e-measure`.
 *
 * A test fails only when its measurement did not happen (the keys did not
 * reach the editor, the result did not arrive); the thresholds are verdicts
 * for the report, not assertions.
 */

const CONDITIONS = [
  { name: "unthrottled", rate: 1 },
  { name: "cpu-4x", rate: 4 },
] as const;
type Condition = (typeof CONDITIONS)[number];

const REPEATS = Number(process.env.E2E_MEASURE_REPEATS ?? 3);

/** Above this one-minute load average the machine is too busy to measure on. */
const LOAD_CEILING = os.cpus().length * 0.6;
const LOAD_WAIT_MS = Number(process.env.E2E_MEASURE_LOAD_WAIT_MINUTES ?? 30) * 60_000;

/** About twelve characters a second: a fast typist, not a paste. */
const KEY_GAP_MS = 80;

/** A long task, by the Long Tasks API's own definition. */
const LONG_TASK_MS = 50;

const OUT = process.env.E2E_MEASURE_OUT ?? path.resolve(__dirname, "../test-results/play-measurements.json");
const TRACE_DIR = process.env.E2E_MEASURE_TRACE;
const SESSION_FILE = path.join(STATE_DIR, "measure-session.json");

test.describe.configure({ mode: "serial" });

/* ---- What the page records ------------------------------------------- */

type LongTask = { start: number; duration: number };
type EventEntry = { name: string; start: number; duration: number; processing: number; interactionId: number };
type Frame = { start: number; duration: number; blocking: number; scripts: { source: string; duration: number }[] };
type Recorded = {
  longtasks: LongTask[];
  events: EventEntry[];
  frames: Frame[];
  lcp: { start: number; size: number; element: string }[];
  fcp: number | null;
  /** Entry types this browser does not offer: a measurement that cannot be made. */
  missing: string[];
};

declare global {
  interface Window {
    __measure: Recorded;
  }
}

/**
 * Observers installed before any of the page's own script runs, so the first
 * load's long tasks and paints are seen too. Event entries are kept only for
 * interactions; a pointer move is not one.
 */
function installObservers(): void {
  const m: Recorded = { longtasks: [], events: [], frames: [], lcp: [], fcp: null, missing: [] };
  window.__measure = m;
  const observe = (type: string, onEntry: (entry: PerformanceEntry) => void, extra: object = {}) => {
    // Chromium only warns about a type it does not know, and an observer that
    // never fires would read as "no long tasks".
    if (!PerformanceObserver.supportedEntryTypes.includes(type)) {
      m.missing.push(type);
      return;
    }
    new PerformanceObserver((list) => list.getEntries().forEach(onEntry)).observe({ type, buffered: true, ...extra });
  };
  observe("longtask", (e) => m.longtasks.push({ start: e.startTime, duration: e.duration }));
  observe(
    "event",
    (e) => {
      // interactionId is newer than this TypeScript's DOM library.
      const timing = e as PerformanceEventTiming & { interactionId?: number };
      const id = timing.interactionId ?? 0;
      if (id > 0) {
        m.events.push({
          name: e.name,
          start: e.startTime,
          duration: e.duration,
          processing: timing.processingEnd - timing.processingStart,
          interactionId: id,
        });
      }
    },
    { durationThreshold: 16 },
  );
  observe("long-animation-frame", (e) => {
    const frame = e as PerformanceEntry & {
      blockingDuration: number;
      scripts: { invoker: string; sourceURL: string; sourceFunctionName: string; duration: number }[];
    };
    m.frames.push({
      start: frame.startTime,
      duration: frame.duration,
      blocking: frame.blockingDuration,
      scripts: frame.scripts.map((s) => ({
        source: `${s.invoker} ${s.sourceFunctionName || "?"} ${s.sourceURL.replace(/^.*\/_next\//, "")}`.trim(),
        duration: s.duration,
      })),
    });
  });
  observe("largest-contentful-paint", (e) => {
    const lcp = e as PerformanceEntry & { size: number; element: Element | null };
    // Described now: the element may be gone from the page by the time it is read.
    const text = (lcp.element?.textContent ?? "").replace(/\s+/g, " ").trim().slice(0, 40);
    const element = lcp.element ? `${lcp.element.tagName.toLowerCase()}${text ? ` "${text}"` : ""}` : "";
    m.lcp.push({ start: lcp.startTime, size: lcp.size, element });
  });
  observe("paint", (e) => {
    if (e.name === "first-contentful-paint") m.fcp = e.startTime;
  });
}

async function now(page: Page): Promise<number> {
  return page.evaluate(() => performance.now());
}

/**
 * Waits until the page has had `quietMs` without a long task (at most
 * `maxMs`), then two frames for the last event entries to be delivered.
 */
async function settle(page: Page, quietMs = 1_000, maxMs = 15_000): Promise<void> {
  await page.evaluate(
    async ({ quietMs, maxMs }) => {
      const begun = performance.now();
      const lastEnd = () => {
        const tasks = window.__measure.longtasks;
        const last = tasks[tasks.length - 1];
        return last ? last.start + last.duration : 0;
      };
      while (performance.now() - begun < maxMs && performance.now() - Math.max(lastEnd(), begun) < quietMs) {
        await new Promise((resolve) => setTimeout(resolve, 100));
      }
      await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    },
    { quietMs, maxMs },
  );
}

async function recorded(page: Page, from: number, to: number) {
  return page.evaluate(
    ({ from, to }) => {
      const m = window.__measure;
      const within = (start: number) => start >= from && start <= to;
      return {
        longtasks: m.longtasks.filter((t) => within(t.start)),
        events: m.events.filter((e) => within(e.start)),
        frames: m.frames.filter((f) => within(f.start)),
      };
    },
    { from, to },
  );
}

/* ---- Statistics -------------------------------------------------------- */

function percentile(sorted: number[], p: number): number {
  if (sorted.length === 0) return 0;
  return sorted[Math.min(sorted.length - 1, Math.ceil((p / 100) * sorted.length) - 1)];
}

/**
 * Per-interaction latency as INP defines it: the longest event entry of each
 * interaction. Entries under 16 ms are not reported by the browser, so an
 * interaction that left none counts as 0 here and reads "<16" in the report.
 */
function interactions(events: EventEntry[], names: readonly string[], expected: number) {
  const byId = new Map<number, number>();
  const handlers = new Map<number, number>();
  for (const e of events) {
    if (!names.includes(e.name)) continue;
    byId.set(e.interactionId, Math.max(byId.get(e.interactionId) ?? 0, e.duration));
    handlers.set(e.interactionId, (handlers.get(e.interactionId) ?? 0) + e.processing);
  }
  const handlerTimes = [...handlers.values()].sort((a, b) => a - b);
  const durations = [...byId.values()];
  while (durations.length < expected) durations.push(0);
  durations.sort((a, b) => a - b);
  return {
    count: expected,
    observed: byId.size,
    p50: percentile(durations, 50),
    p95: percentile(durations, 95),
    max: durations[durations.length - 1] ?? 0,
    over200: durations.filter((d) => d >= 200).length,
    /** The event handlers' own time, of the interactions the browser reported. */
    handlerMax: Math.round(handlerTimes[handlerTimes.length - 1] ?? 0),
  };
}

function longTaskStats(tasks: LongTask[]) {
  const durations = tasks.map((t) => t.duration).sort((a, b) => a - b);
  return {
    longTasks: durations.length,
    longestTask: durations[durations.length - 1] ?? 0,
    blocking: durations.reduce((sum, d) => sum + Math.max(0, d - LONG_TASK_MS), 0),
    tasksOver200: durations.filter((d) => d > 200).length,
  };
}

/** The script attributions of the slowest frames, summed by source. */
function topScripts(frames: Frame[], limit = 6) {
  const bySource = new Map<string, number>();
  for (const frame of frames) {
    for (const script of frame.scripts) bySource.set(script.source, (bySource.get(script.source) ?? 0) + script.duration);
  }
  return [...bySource.entries()]
    .sort((a, b) => b[1] - a[1])
    .slice(0, limit)
    .map(([source, duration]) => ({ source, duration: Math.round(duration) }));
}

const KEY_EVENTS = ["keydown", "keypress", "keyup", "input", "beforeinput"] as const;
const POINTER_EVENTS = ["pointerdown", "pointerup", "mousedown", "mouseup", "click"] as const;

/* ---- The run record ----------------------------------------------------- */

type Run = {
  scenario: string;
  condition: Condition["name"];
  repeat: number;
  load: number[];
  /** The one-minute load when the run ended; over LOAD_CEILING marks it noisy. */
  loadEnd: number;
  noisy: boolean;
  traced: boolean;
  stats: Record<string, number>;
  /** What a run observed that is not a number, such as the LCP element. */
  notes?: Record<string, string>;
  scripts?: { source: string; duration: number }[];
};

const runs: Run[] = [];

/** The median of each figure over a scenario's repeats under one condition, with its spread. */
function summarise(all: Run[]) {
  const groups = new Map<string, Run[]>();
  for (const run of all) {
    const key = `${run.scenario}\u0000${run.condition}`;
    groups.set(key, [...(groups.get(key) ?? []), run]);
  }
  return [...groups.values()].map((group) => ({
    scenario: group[0].scenario,
    condition: group[0].condition,
    repeats: group.length,
    noisyRuns: group.filter((run) => run.noisy).length,
    notes: Object.fromEntries(
      Object.keys(group[0].notes ?? {}).map((key) => [key, [...new Set(group.map((run) => run.notes?.[key] ?? ""))]]),
    ),
    stats: Object.fromEntries(
      Object.keys(group[0].stats).map((key) => {
        const values = group.map((run) => run.stats[key]).sort((a, b) => a - b);
        const round = (n: number) => Math.round(n * 10) / 10;
        return [key, { median: round(percentile(values, 50)), min: round(values[0]), max: round(values[values.length - 1]) }];
      }),
    ),
  }));
}

/** Waits for the machine to be quiet enough and returns the load it measured at. */
async function waitForQuietMachine(): Promise<number[]> {
  const deadline = Date.now() + LOAD_WAIT_MS;
  for (;;) {
    const load = os.loadavg().map((l) => Math.round(l * 100) / 100);
    if (load[0] <= LOAD_CEILING) return load;
    if (Date.now() > deadline) {
      throw new Error(`the one-minute load stayed above ${LOAD_CEILING} for ${LOAD_WAIT_MS / 60_000} minutes; not measuring`);
    }
    console.log(`measure: load ${load[0]} is above ${LOAD_CEILING}; waiting`);
    await new Promise((resolve) => setTimeout(resolve, 15_000));
  }
}

/**
 * Runs `measure` REPEATS times under each condition, alternating conditions
 * so that drift in the machine falls on both alike, each in a fresh context
 * with an empty cache and the CPU throttled as the condition says.
 */
async function eachCondition(
  browser: Browser,
  scenario: string,
  measure: (page: Page) => Promise<{ stats: Record<string, number>; frames?: Frame[]; notes?: Record<string, string> }>,
): Promise<void> {
  for (let repeat = 1; repeat <= REPEATS; repeat++) {
    for (const condition of CONDITIONS) {
      const load = await waitForQuietMachine();
      const context = await browser.newContext({ ...contextOptions(), storageState: SESSION_FILE });
      await context.addInitScript(installObservers);
      const page = await context.newPage();
      // The development server's hot-reload channel: its numbers mean nothing.
      let devServer = false;
      const isDevServer = (url: string) => {
        if (url.includes("/_next/webpack-hmr")) devServer = true;
      };
      page.on("request", (request) => isDevServer(request.url()));
      page.on("websocket", (socket) => isDevServer(socket.url()));
      const cdp = await context.newCDPSession(page);
      await cdp.send("Emulation.setCPUThrottlingRate", { rate: condition.rate });

      const traced = Boolean(TRACE_DIR) && repeat === 1;
      if (traced) {
        mkdirSync(TRACE_DIR!, { recursive: true });
        await browser.startTracing(page, {
          path: path.join(TRACE_DIR!, `${scenario}-${condition.name}.json`),
          categories: [
            "devtools.timeline",
            "disabled-by-default-devtools.timeline",
            "disabled-by-default-devtools.timeline.frame",
            "v8.execute",
            "disabled-by-default-v8.cpu_profiler",
            "blink.user_timing",
            "loading",
          ],
        });
      }
      try {
        const { stats, frames, notes } = await measure(page);
        // The gate is checked before a run; a machine that got busy during it is flagged, not hidden.
        const loadEnd = Math.round(os.loadavg()[0] * 100) / 100;
        expect(devServer, "the stack serves a development build; measure a production one").toBe(false);
        const missing = await page.evaluate(() => window.__measure.missing);
        expect(missing, "this browser cannot report these entry types").toEqual([]);
        stats.calibrationMs = await calibrate(page);
        runs.push({
          scenario,
          condition: condition.name,
          repeat,
          load,
          loadEnd,
          noisy: loadEnd > LOAD_CEILING,
          traced,
          stats,
          notes,
          scripts: frames ? topScripts(frames) : undefined,
        });
        const flag = loadEnd > LOAD_CEILING ? ` NOISY (load ${loadEnd} at the end)` : "";
        console.log(
          `measure: ${scenario} ${condition.name} #${repeat} load ${load[0]}${flag} ${JSON.stringify(stats)}` +
            (notes ? ` ${JSON.stringify(notes)}` : ""),
        );
      } finally {
        if (traced) await browser.stopTracing();
        await context.close();
      }
    }
  }
}

/**
 * A fixed piece of work timed in the page, so each run shows the throttle it
 * really ran under: about four times the unthrottled figure at 4×.
 */
async function calibrate(page: Page): Promise<number> {
  return page.evaluate(() => {
    const begun = performance.now();
    let x = 0;
    for (let i = 0; i < 20_000_000; i++) x = (x + i * 7) % 1_000_003;
    // Using x keeps the loop from being optimised away; it is never negative.
    return Math.round(performance.now() - begun + (x < 0 ? 1 : 0));
  });
}

let contextOptions: () => Parameters<Browser["newContext"]>[0] = () => ({});

/* ---- The play screen ---------------------------------------------------- */

function contest() {
  return readSeed().contest;
}

function editor(page: Page) {
  return page.getByRole("textbox", { name: "Your query" });
}

function sqlTabs(page: Page) {
  return page.getByRole("tablist", { name: "SQL tabs" }).getByRole("tab");
}

async function openPlay(page: Page): Promise<void> {
  await page.goto(`/contests/${contest().id}/play`);
  await expect(editor(page)).toHaveAttribute("contenteditable", "true");
  await settle(page);
}

/** Replaces the editor's whole text, leaving the caret after `before`. */
async function setEditorText(page: Page, before: string, after = ""): Promise<void> {
  await editor(page).focus();
  await page.keyboard.press("ControlOrMeta+a");
  if (after) {
    await page.keyboard.insertText(after);
    await page.keyboard.press("ControlOrMeta+Home");
  }
  await page.keyboard.insertText(before);
  await page.keyboard.press("Escape");
}

/** A short query, the kind a participant starts from. */
const SHORT_DOCUMENT = "SELECT name, alibi\nFROM suspects\nORDER BY id;\n";

/**
 * About five hundred lines of the SQL a participant accumulates over a
 * contest: commented CTEs, joins, aggregates and scratch queries.
 */
function longDocument(lines = 500): string {
  const blocks: string[] = [];
  let n = 0;
  while (blocks.join("\n").split("\n").length < lines) {
    n++;
    blocks.push(
      [
        `-- Attempt ${n}: who was near room ${n % 9} without an alibi?`,
        `WITH seen_${n} AS (`,
        `    SELECT s.id, s.name, count(*) AS sightings, max(s.alibi) AS alibi`,
        `    FROM suspects AS s`,
        `    LEFT JOIN suspects AS other ON other.id <> s.id AND other.alibi IS NULL`,
        `    WHERE s.name ILIKE '%${String.fromCharCode(97 + (n % 26))}%'`,
        `    GROUP BY s.id, s.name`,
        `), ranked_${n} AS (`,
        `    SELECT id, name, sightings,`,
        `           rank() OVER (ORDER BY sightings DESC, name) AS place`,
        `    FROM seen_${n}`,
        `    WHERE alibi IS NULL OR sightings > ${n % 4}`,
        `)`,
        `SELECT r.place, r.name, r.sightings, coalesce(s.alibi, 'none') AS alibi`,
        `FROM ranked_${n} AS r`,
        `JOIN suspects AS s USING (id)`,
        `WHERE r.place <= ${3 + (n % 5)}`,
        `ORDER BY r.place, r.name;`,
        ``,
      ].join("\n"),
    );
  }
  return blocks.join("\n").split("\n").slice(0, lines).join("\n") + "\n";
}

/** What is typed in each typing run: well over 200 keystrokes of real SQL. */
const TYPED =
  "-- typing probe\n" +
  "SELECT s.name, s.alibi, count(*) AS seen\n" +
  "FROM suspects AS s\n" +
  "WHERE s.alibi IS NULL OR s.name LIKE 'M%'\n" +
  "GROUP BY s.name, s.alibi\n" +
  "ORDER BY seen DESC, s.name\n" +
  "LIMIT 10;\n" +
  "SELECT id, upper(name) AS shout FROM suspects WHERE id > 1 ORDER BY id;\n";

async function typeAndMeasure(page: Page) {
  const from = await now(page);
  for (const key of TYPED) {
    await page.keyboard.type(key);
    await page.waitForTimeout(KEY_GAP_MS);
  }
  await settle(page);
  const to = await now(page);
  // The keys reached the editor, not some other element.
  await expect(editor(page)).toContainText("typing probe");
  const { events, longtasks, frames } = await recorded(page, from, to);
  const keys = interactions(events, KEY_EVENTS, TYPED.length);
  return {
    stats: {
      keystrokes: keys.count,
      observed: keys.observed,
      p50: keys.p50,
      p95: keys.p95,
      max: keys.max,
      over200: keys.over200,
      handlerMax: keys.handlerMax,
      ...longTaskStats(longtasks),
    },
    frames,
  };
}

/**
 * The Run button's press and what follows it, until the answer is drawn and
 * the page is quiet. The button is disabled from the press until the answer
 * is committed, so its coming back marks the answer on screen.
 */
async function runAndMeasure(page: Page) {
  const run = page.getByRole("button", { name: "Run", exact: true });
  const from = await now(page);
  const answered = page.waitForResponse(
    (response) => response.request().method() === "POST" && /\/contests\/[^/]+\/query$/.test(response.url()),
  );
  const started = Date.now();
  await run.click();
  const response = await answered;
  expect(response.ok(), `the query answered ${response.status()}`).toBe(true);
  const roundTrip = Date.now() - started;
  await expect(run).toBeEnabled();
  const shown = Date.now() - started;
  await settle(page);
  const to = await now(page);
  const { events, longtasks, frames } = await recorded(page, from, to);
  const press = interactions(events, POINTER_EVENTS, 1);
  return { stats: { press: press.max, pressHandler: press.handlerMax, roundTrip, shown, ...longTaskStats(longtasks) }, frames };
}

/** One row-count figure in the meter above the table. */
async function meterRows(page: Page): Promise<number> {
  const meter = page.getByText(/^rows \d+$/i);
  return Number((await meter.textContent())!.replace(/\D+/g, ""));
}

/**
 * At most QUERY_MAX_ROWS (1000 in the deployment) rows reach the browser;
 * the series is longer so that the answer is cut there, as a careless
 * participant's would be.
 */
const LARGE_QUERY =
  "SELECT g AS id, md5(g::text) AS hash, 'suspect ' || g AS name,\n" +
  "       now() - g * interval '1 minute' AS seen_at, g % 7 = 0 AS flagged,\n" +
  "       repeat(md5(g::text), 3) AS note\n" +
  "FROM generate_series(1, 5000) AS g;";

/**
 * The heaviest answer the budget lets through: a thousand rows of ten
 * 400-character columns, about 4 MB against QUERY_MAX_BYTES' 5 MiB. Every
 * cell is under CELL_CLIP_CHARS, so all of it is laid out.
 */
const WIDE_QUERY =
  "SELECT g AS id,\n" +
  Array.from({ length: 10 }, (_, i) => `       repeat(md5((g + ${i})::text), 12) AS c${i}`).join(",\n") +
  "\nFROM generate_series(1, 1000) AS g;";

/* ---- The measurements --------------------------------------------------- */

test.beforeAll(async ({ browser, baseURL, ignoreHTTPSErrors, viewport, locale, userAgent, deviceScaleFactor }) => {
  contextOptions = () => ({ baseURL, ignoreHTTPSErrors, viewport, locale, userAgent, deviceScaleFactor });
  const context = await browser.newContext(contextOptions());
  const page = await context.newPage();
  await signIn(page, readSeed().participant);
  mkdirSync(STATE_DIR, { recursive: true, mode: 0o700 });
  await context.storageState({ path: SESSION_FILE });
  await context.close();
});

test.afterAll(async ({ browser }) => {
  rmSync(SESSION_FILE, { force: true });
  mkdirSync(path.dirname(OUT), { recursive: true });
  const meta = {
    when: new Date().toISOString(),
    browser: browser.version(),
    cores: os.cpus().length,
    loadCeiling: LOAD_CEILING,
    repeats: REPEATS,
    keyGapMs: KEY_GAP_MS,
    typedKeystrokes: TYPED.length,
  };
  const summary = summarise(runs.filter((run) => !run.traced));
  writeFileSync(OUT, JSON.stringify({ meta, summary, runs }, null, 2));
  for (const line of summary) {
    const figures = Object.entries(line.stats).map(([key, { median, min, max }]) => `${key} ${median} [${min}–${max}]`);
    const noisy = line.noisyRuns ? ` (${line.noisyRuns} noisy)` : "";
    const notes = Object.entries(line.notes).map(([key, values]) => `${key} ${values.join(" | ")}`);
    console.log(`measure: median ${line.scenario} ${line.condition}${noisy}: ${[...figures, ...notes].join(", ")}`);
  }
  console.log(`measure: ${runs.length} runs written to ${OUT}`);
});

test("first load of the play screen", async ({ browser }) => {
  await eachCondition(browser, "first-load", async (page) => {
    await page.goto(`/contests/${contest().id}/play`);
    const editorReady = await page
      .waitForFunction(
        () => document.querySelector('[aria-label="Your query"][contenteditable="true"]') && performance.now(),
        undefined,
        { polling: "raf", timeout: 60_000 },
      )
      .then((handle) => handle.jsonValue() as Promise<number>);
    await page.waitForLoadState("load");
    // Lighthouse's interactive point: five seconds without a long task.
    await settle(page, 5_000, 60_000);
    const m = await page.evaluate(() => window.__measure);
    const fcp = m.fcp ?? 0;
    const lastLcp = m.lcp[m.lcp.length - 1];
    const afterFcp = m.longtasks.filter((t) => t.start + t.duration > fcp);
    const interactive = afterFcp.length ? Math.max(...afterFcp.map((t) => t.start + t.duration)) : fcp;
    const tbt = afterFcp.reduce((sum, t) => {
      const counted = t.start + t.duration - Math.max(t.start, fcp);
      return sum + Math.max(0, counted - LONG_TASK_MS);
    }, 0);
    expect(fcp, "no first contentful paint was recorded").toBeGreaterThan(0);
    expect(lastLcp, "no largest contentful paint was recorded").toBeDefined();
    return {
      stats: {
        fcp,
        lcp: lastLcp.start,
        editorReady,
        interactive,
        tbt,
        ...longTaskStats(m.longtasks),
      },
      // LCP can land on the page's shell rather than the editor; editorReady
      // is when the content a participant works in is there.
      notes: { lcpElement: lastLcp.element || "(none)" },
      frames: m.frames,
    };
  });
});

test("typing in a short document", async ({ browser }) => {
  await eachCondition(browser, "typing-short", async (page) => {
    await openPlay(page);
    await setEditorText(page, SHORT_DOCUMENT);
    await settle(page);
    return typeAndMeasure(page);
  });
});

test("typing in the middle of a 500-line document", async ({ browser }) => {
  const document = longDocument();
  const lines = document.split("\n");
  const middle = Math.floor(lines.length / 2);
  const before = lines.slice(0, middle).join("\n") + "\n";
  const after = lines.slice(middle).join("\n");
  await eachCondition(browser, "typing-500-lines", async (page) => {
    await openPlay(page);
    await setEditorText(page, before, after);
    await settle(page);
    return typeAndMeasure(page);
  });
});

test("running a query", async ({ browser }) => {
  await eachCondition(browser, "run", async (page) => {
    await openPlay(page);
    await setEditorText(page, "SELECT id, name, alibi FROM suspects ORDER BY id;");
    await settle(page);
    const all: Record<string, number>[] = [];
    const frames: Frame[] = [];
    for (let i = 0; i < 5; i++) {
      const one = await runAndMeasure(page);
      expect(await meterRows(page)).toBe(3);
      all.push(one.stats);
      frames.push(...one.frames);
    }
    const max = (key: string) => Math.max(...all.map((s) => s[key]));
    const sum = (key: string) => all.reduce((total, s) => total + s[key], 0);
    const sorted = (key: string) => all.map((s) => s[key]).sort((a, b) => a - b);
    return {
      stats: {
        runs: all.length,
        pressMax: max("press"),
        roundTripP50: percentile(sorted("roundTrip"), 50),
        shownP50: percentile(sorted("shown"), 50),
        shownMax: max("shown"),
        longTasks: sum("longTasks"),
        longestTask: max("longestTask"),
        blocking: sum("blocking"),
        tasksOver200: sum("tasksOver200"),
      },
      frames,
    };
  });
});

for (const [name, query] of [
  ["large-result", LARGE_QUERY],
  ["wide-result", WIDE_QUERY],
] as const) {
  test(`rendering and scrolling a ${name.replace("-", " ")}`, async ({ browser }) => {
    await eachCondition(browser, name, async (page) => {
      await openPlay(page);
      await setEditorText(page, query);
      await settle(page);
      const render = await runAndMeasure(page);
      expect(await meterRows(page)).toBe(1000);

      // The table keeps only the rows in view in the DOM; scrolling is what
      // makes it render.
      const table = page.getByRole("table").filter({ has: page.getByRole("columnheader", { name: /^id\b/ }) });
      const box = (await table.boundingBox())!;
      // Near the table's top left: a wide answer runs past the viewport, and
      // a wheel turned off-screen scrolls nothing.
      await page.mouse.move(box.x + Math.min(box.width / 2, 300), box.y + Math.min(box.height / 2, 200));
      const from = await now(page);
      await page.evaluate(() => {
        const intervals: number[] = [];
        let last = performance.now();
        const tick = (t: number) => {
          intervals.push(t - last);
          last = t;
          if (!(window as unknown as { __stopFrames?: boolean }).__stopFrames) requestAnimationFrame(tick);
        };
        (window as unknown as { __frameIntervals: number[] }).__frameIntervals = intervals;
        requestAnimationFrame(tick);
      });
      for (let i = 0; i < 60; i++) {
        await page.mouse.wheel(0, 400);
        await page.waitForTimeout(40);
      }
      // The ids are the only cells that are a bare number.
      await expect(table.getByRole("cell", { name: /^[3-9]\d\d$/ }).first()).toBeVisible();
      for (let i = 0; i < 30; i++) {
        await page.mouse.wheel(0, -400);
        await page.waitForTimeout(40);
      }
      const intervals = await page.evaluate(() => {
        (window as unknown as { __stopFrames?: boolean }).__stopFrames = true;
        return (window as unknown as { __frameIntervals: number[] }).__frameIntervals;
      });
      await settle(page);
      const to = await now(page);
      const scroll = await recorded(page, from, to);
      const sortedIntervals = [...intervals].sort((a, b) => a - b);
      const scrollTasks = longTaskStats(scroll.longtasks);
      return {
        stats: {
          pressMs: render.stats.press,
          shown: render.stats.shown,
          renderLongTasks: render.stats.longTasks,
          renderLongestTask: render.stats.longestTask,
          renderBlocking: render.stats.blocking,
          scrollLongTasks: scrollTasks.longTasks,
          scrollLongestTask: scrollTasks.longestTask,
          scrollBlocking: scrollTasks.blocking,
          frameP50: percentile(sortedIntervals, 50),
          frameP95: percentile(sortedIntervals, 95),
          framesOver50: intervals.filter((d) => d > 50).length,
          frames: intervals.length,
        },
        frames: [...render.frames, ...scroll.frames],
      };
    });
  });
}

test("switching SQL tabs", async ({ browser }) => {
  const document = longDocument();
  await eachCondition(browser, "tab-switch", async (page) => {
    await openPlay(page);
    if ((await sqlTabs(page).count()) < 2) await newSqlTab(page);
    await sqlTabs(page).nth(0).click();
    await setEditorText(page, SHORT_DOCUMENT);
    await sqlTabs(page).nth(1).click();
    await setEditorText(page, document);
    await settle(page);

    const from = await now(page);
    const switches = 10;
    for (let i = 0; i < switches; i++) {
      const tab = sqlTabs(page).nth(i % 2);
      await tab.click();
      await expect(tab).toHaveAttribute("aria-selected", "true");
      await page.waitForTimeout(300);
    }
    await settle(page);
    const to = await now(page);
    const { events, longtasks, frames } = await recorded(page, from, to);
    const clicks = interactions(events, POINTER_EVENTS, switches);
    return {
      stats: { switches, p50: clicks.p50, p95: clicks.p95, max: clicks.max, handlerMax: clicks.handlerMax, ...longTaskStats(longtasks) },
      frames,
    };
  });
});

test("opening the side panel and its tabs", async ({ browser }) => {
  await eachCondition(browser, "side-panel", async (page) => {
    await openPlay(page);
    const toggle = page.getByRole("button", { name: "Questions panel" });
    await expect(toggle).toHaveAttribute("aria-pressed", "true");

    const from = await now(page);
    let presses = 0;
    for (let i = 0; i < 4; i++) {
      for (const showing of ["false", "true"]) {
        await toggle.click();
        await expect(toggle).toHaveAttribute("aria-pressed", showing);
        presses++;
        await page.waitForTimeout(300);
      }
    }
    const sideTabs = page
      .getByRole("tablist")
      .filter({ has: page.getByRole("tab", { name: "Story", exact: true }) })
      .getByRole("tab");
    const count = await sideTabs.count();
    for (let round = 0; round < 2; round++) {
      for (let i = 0; i < count; i++) {
        const tab = sideTabs.nth(i);
        await tab.click();
        await expect(tab).toHaveAttribute("aria-selected", "true");
        presses++;
        await page.waitForTimeout(300);
      }
    }
    await settle(page);
    const to = await now(page);
    const { events, longtasks, frames } = await recorded(page, from, to);
    const clicks = interactions(events, POINTER_EVENTS, presses);
    return {
      stats: { presses, p50: clicks.p50, p95: clicks.p95, max: clicks.max, handlerMax: clicks.handlerMax, ...longTaskStats(longtasks) },
      frames,
    };
  });
});
