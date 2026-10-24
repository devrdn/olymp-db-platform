import { expect, test, type Locator, type Page } from "@playwright/test";

import { readSeed } from "./api";
import {
  capture,
  chooseTheme,
  closeSqlTab,
  editorText,
  newSqlTab,
  openPlay,
  openSideTab,
  runQuery,
  scrollWithin,
  signIn,
} from "./support";

/**
 * The play screen's post-beta behaviour, in the features contest
 * (seed-features.ts): the read budget, the theme switch, the wider side
 * column, the query log, copying rows, a table click, completion, a titled
 * question, the Tables tab and the diagram. Nothing here answers a question,
 * so the contest stays as the seed left it for the specs after this one.
 */

test.use({ viewport: { width: 1600, height: 900 } });

const seed = () => readSeed().features;

/** The result table under the console, told apart by its first column. */
function resultTable(page: Page, column: RegExp): Locator {
  return page.getByRole("table").filter({ has: page.getByRole("columnheader", { name: column }) });
}

/** Whether a line of the console's hint is drawn over the Result toggle. */
async function hintRunsIntoResult(page: Page): Promise<boolean> {
  const result = (await page.getByRole("button", { name: "Result", exact: true }).boundingBox())!;
  const lines = await page
    .getByRole("main")
    .getByText("One statement at a time.")
    .evaluate((hint) => {
      const range = document.createRange();
      range.selectNodeContents(hint);
      return [...range.getClientRects()].map(({ x, y, width, height }) => ({ x, y, width, height }));
    });
  return lines.some(
    (line) =>
      line.x < result.x + result.width &&
      line.x + line.width > result.x &&
      line.y < result.y + result.height &&
      line.y + line.height > result.y,
  );
}

test("reloading the play page ten times leaves the console free to run queries", async ({ page }) => {
  const { accounts, contests } = seed();
  await signIn(page, accounts.bob);

  // Ten loads read the story, questions, log, schema, workspace, events and
  // clarifications each: well past the thirty a minute the queries once shared.
  for (let load = 0; load < 10; load++) {
    await openPlay(page, contests.features!.id);
  }
  await expect(page.getByText("The contest page is being loaded too often", { exact: false })).toHaveCount(0);
  // The events channel was admitted too: the clock has its first sync.
  await expect(page.getByRole("timer")).toHaveText(/^\d+:\d\d:\d\d$/);

  for (const id of [1, 2, 3]) {
    await runQuery(page, `SELECT name FROM suspects WHERE id = ${id}`);
    await expect(resultTable(page, /name/).getByRole("row")).toHaveCount(2);
  }
  await expect(page.getByText("too often", { exact: false })).toHaveCount(0);
});

test("the theme switches in place: same scroll, same editor text, no navigation", async ({ page }) => {
  const { accounts, contests } = seed();
  await signIn(page, accounts.alice);
  await openPlay(page, contests.features!.id);

  await runQuery(page, "SELECT g FROM generate_series(1, 200) AS g");
  const table = resultTable(page, /^g/);
  await expect(table).toHaveAttribute("aria-rowcount", "201");
  const before = await scrollWithin(table, 900);
  expect(before).toBeGreaterThan(0);
  const text = await editorText(page);

  const documents: string[] = [];
  const writes: string[] = [];
  page.on("request", (request) => {
    if (request.resourceType() === "document") documents.push(request.url());
    if (request.method() !== "GET") writes.push(`${request.method()} ${request.url()}`);
  });
  await page.evaluate(() => Object.assign(window, { stillTheSamePage: true }));

  // Through the app bar's toggle, each way.
  await chooseTheme(page, "dark");
  await chooseTheme(page, "light");

  expect(await page.evaluate(() => (window as { stillTheSamePage?: boolean }).stillTheSamePage)).toBe(true);
  expect(await scrollWithin(table)).toBe(before);
  expect(await editorText(page)).toBe(text);
  expect(documents).toEqual([]);
  // The workspace may autosave a tab; a theme press itself posts nothing.
  expect(writes.filter((write) => !write.includes("/play/workspace"))).toEqual([]);
});

test("the side column widens to sixty percent of the window, and a stored width is drawn within it", async ({
  page,
}) => {
  const { accounts, contests } = seed();
  const id = contests.features!.id;
  await signIn(page, accounts.alice);
  await openPlay(page, id);

  const handle = page.getByRole("separator", { name: "Width of the questions panel" });
  expect(await hintRunsIntoResult(page)).toBe(false);
  await handle.focus();
  for (let press = 0; press < 15; press++) await page.keyboard.press("Shift+ArrowLeft");
  await expect(handle).toHaveAttribute("aria-valuemax", "60");
  await expect(handle).toHaveAttribute("aria-valuenow", "60");
  const box = (await handle.boundingBox())!;
  // 60rem of 16px beside the handle: sixty percent of 1600px.
  expect(Math.abs(1600 - (box.x + box.width) - 960)).toBeLessThanOrEqual(8);
  await capture(page, "play-side-60pct");

  await page.evaluate(
    (key) => localStorage.setItem(key, JSON.stringify({ schema: 13.25, side: 200 })),
    `dbcontest.console.panes.${id}`,
  );
  await openPlay(page, id);
  await expect(handle).toHaveAttribute("aria-valuenow", "60");

  for (const width of [1280, 1024]) {
    await page.setViewportSize({ width, height: 900 });
    // Sixty percent of the window, or less if the schema and the console's
    // sixteen rem would not fit beside it.
    const rem = width / 16;
    const bound = String(Math.min(Math.floor(rem * 0.6), Math.floor(rem - 13.25 - 16)));
    await expect(handle).toHaveAttribute("aria-valuemax", bound);
    await expect(handle).toHaveAttribute("aria-valuenow", bound);
    const drawn = (await handle.boundingBox())!;
    expect(width - (drawn.x + drawn.width)).toBeLessThanOrEqual(width * 0.6 + 8);
    // The console keeps its sixteen rem beside a stored width of 200.
    const schema = (await page.getByRole("separator", { name: "Width of the schema panel" }).boundingBox())!;
    expect(drawn.x - (schema.x + schema.width)).toBeGreaterThanOrEqual(16 * 16);
    // ...and in that width its toolbar still reads: the hint beside Run does
    // not run into the Result toggle.
    const overlaps = await hintRunsIntoResult(page);
    expect.soft(overlaps, `the toolbar's hint runs into the Result toggle at ${width}px`).toBe(false);
    await capture(page, `play-side-stored-200-at-${width}`);
  }
  // Drawn at the bound, the stored width itself waits for the next drag.
  expect(await page.evaluate((key) => localStorage.getItem(key), `dbcontest.console.panes.${id}`)).toContain(
    '"side":200',
  );
  await page.evaluate((key) => localStorage.removeItem(key), `dbcontest.console.panes.${id}`);
});

test.describe("with the clipboard", () => {
  test.use({ permissions: ["clipboard-read", "clipboard-write"] });

  test("a query-log row opens to its whole statement, and Copy copies all of it", async ({ page }) => {
    const { accounts, contests } = seed();
    await signIn(page, accounts.alice);
    await openPlay(page, contests.features!.id);

    // Past the thousand characters the log's list keeps of a statement.
    const sql = `SELECT name /* ${"padding ".repeat(150)}*/ FROM suspects WHERE id = 2 -- the-tail-of-a-long-query`;
    await runQuery(page, sql);
    await expect(resultTable(page, /name/).getByRole("row")).toHaveCount(2);

    await page.getByRole("button", { name: "Query log" }).click();
    const row = page.getByRole("row").filter({ hasText: "SELECT name /* padding" }).first();
    const show = row.getByRole("button", { name: "Show" });
    await expect(show).toHaveAttribute("aria-expanded", "false");
    await expect(row).not.toContainText("the-tail-of-a-long-query");
    await show.click();

    const whole = page.getByRole("region", { name: "The whole query" });
    await expect(whole).toContainText("the-tail-of-a-long-query");
    await expect(row.getByRole("button", { name: "Hide" })).toHaveAttribute("aria-expanded", "true");
    await capture(page, "play-query-log-open");

    await whole.getByRole("button", { name: "Copy" }).click();
    await expect(whole.getByText("Copied", { exact: true })).toBeVisible();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(sql);

    await row.getByRole("button", { name: "Hide" }).click();
    await expect(whole).toBeHidden();
  });

  test("the result copies its first five rows as tab-separated text with a header", async ({ page }) => {
    const { accounts, contests } = seed();
    await signIn(page, accounts.alice);
    await openPlay(page, contests.features!.id);

    await runQuery(page, "SELECT id, name FROM suspects ORDER BY id");
    await expect(resultTable(page, /^id/).getByRole("row")).toHaveCount(8);
    await page.getByRole("button", { name: "Copy first 5 rows" }).click();
    await expect(page.getByRole("status").filter({ hasText: "Copied to the clipboard" })).toBeVisible();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
      ["id\tname", "1\tAlice Moreau", "2\tMallory Kent", "3\tVictor Hale", "4\tIris Vance", "5\tOscar Penn"].join("\n"),
    );
    await capture(page, "play-copy-rows");

    await runQuery(page, "SELECT id FROM suspects WHERE id <= 3 ORDER BY id");
    await expect(resultTable(page, /^id/).getByRole("row")).toHaveCount(4);
    await expect(page.getByRole("button", { name: "Copy all rows" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Copy first 5 rows" })).toHaveCount(0);
  });
});

test("a click on a table's name writes a query on it into the editor and runs nothing", async ({ page }) => {
  const { accounts, contests } = seed();
  await signIn(page, accounts.alice);
  await openPlay(page, contests.features!.id);

  const tab = await newSqlTab(page);
  let runs = 0;
  page.on("request", (request) => {
    if (request.method() === "POST" && request.url().includes("/query")) runs++;
  });

  await page.getByRole("button", { name: "Insert a query on suspects into the editor" }).click();
  await expect.poll(() => editorText(page)).toBe("SELECT * FROM suspects LIMIT 5;");
  await expect(page.getByRole("textbox", { name: "Your query" })).toBeFocused();

  await page.getByRole("button", { name: "Insert a query on CaseFiles into the editor" }).click();
  await expect
    .poll(() => editorText(page))
    .toBe('SELECT * FROM suspects LIMIT 5;\nSELECT * FROM "CaseFiles" LIMIT 5;');
  await capture(page, "play-table-click");
  expect(runs).toBe(0);
  await expect(page.getByText("Run a query to see its result here.")).toBeVisible();
  await closeSqlTab(page, tab);
});

test("completion offers keywords, tables and columns, and Tab, Enter, Escape and Mod-Enter behave", async ({
  page,
}) => {
  const { accounts, contests } = seed();
  await signIn(page, accounts.alice);
  await openPlay(page, contests.features!.id);
  const tab = await newSqlTab(page);
  const editor = page.getByRole("textbox", { name: "Your query" });
  await editor.click();
  const popup = page.getByRole("listbox", { name: "Completions" });

  await page.keyboard.type("SEL");
  await expect(popup.getByRole("option", { name: "SELECT", exact: true })).toBeVisible();
  // The popup ignores keys for a moment after it opens, so a fast typist's
  // Enter still breaks the line; wait that out before accepting.
  await page.waitForTimeout(150);
  await page.keyboard.press("Tab");
  await expect.poll(() => editorText(page)).toBe("SELECT");

  await page.keyboard.type(" * FROM sus");
  await expect(popup.getByRole("option", { name: "suspects" })).toBeVisible();
  await capture(page, "play-completion-table");
  await page.waitForTimeout(150);
  await page.keyboard.press("Enter");
  await expect.poll(() => editorText(page)).toBe("SELECT * FROM suspects");

  await page.keyboard.type(" WHERE ali");
  await expect(popup.getByRole("option", { name: /^alibi/ })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(popup).toBeHidden();
  await expect.poll(() => editorText(page)).toBe("SELECT * FROM suspects WHERE ali");

  // Enter after a word typed whole breaks the line instead of rewriting it.
  await page.keyboard.type("bi IS NULL ORDER BY name");
  await expect(popup).toBeVisible();
  await page.waitForTimeout(150);
  await page.keyboard.press("Enter");
  await page.keyboard.type("LIMIT 5");
  await expect.poll(() => editorText(page)).toBe("SELECT * FROM suspects WHERE alibi IS NULL ORDER BY name\nLIMIT 5");

  // Running with the button closes the popup too, rather than leaving it
  // over the editor.
  await page.keyboard.press("ControlOrMeta+a");
  await page.keyboard.type("SELECT name FROM suspects WHERE i");
  await expect(popup).toBeVisible();
  await page.getByRole("button", { name: "Run", exact: true }).click();
  await expect(page.getByText("From Query", { exact: false })).toBeVisible();
  await capture(page, "play-completion-after-run-button");
  await expect.soft(popup, "the completion popup closes when the Run button runs the query").toBeHidden();
  await page.keyboard.press("Escape");
  await editor.click();

  // Mod-Enter with the popup open runs exactly what was typed.
  await page.keyboard.press("ControlOrMeta+a");
  await page.keyboard.insertText("SELECT name FROM suspects WHERE alibi IS NULL ORDER BY nam");
  await page.keyboard.type("e");
  await expect(popup).toBeVisible();
  await page.keyboard.press("ControlOrMeta+Enter");
  await expect(popup).toBeHidden();
  await expect(resultTable(page, /name/).getByRole("row")).toHaveCount(2);
  await expect(resultTable(page, /name/)).toContainText("Mallory Kent");
  await expect.poll(() => editorText(page)).toBe("SELECT name FROM suspects WHERE alibi IS NULL ORDER BY name");
  await closeSqlTab(page, tab);
});

test("a titled question folds its wording under Details, in place and in a larger dialog", async ({ page }) => {
  const { accounts, contests, questions } = seed();
  await signIn(page, accounts.alice);
  await openPlay(page, contests.features!.id);
  await openSideTab(page, "Questions");

  const panel = page.getByRole("tabpanel", { name: "Questions" });
  await expect(panel.getByRole("heading", { level: 2, name: questions.titled!.title })).toBeVisible();
  const details = panel.getByRole("button", { name: "Details" }).first();
  await expect(details).toHaveAccessibleDescription(questions.titled!.title);
  await expect(details).toHaveAttribute("aria-expanded", "false");
  const wording = panel.getByText("The night porter swears every guest came back before midnight.");
  await expect(wording).toBeHidden();

  await details.click();
  await expect(details).toHaveAttribute("aria-expanded", "true");
  await expect(wording).toBeVisible();
  await expect(panel.getByRole("table").getByRole("columnheader", { name: "Key holder" })).toBeVisible();
  await capture(page, "play-question-details");

  const larger = panel.getByRole("button", { name: "Open larger" }).first();
  await larger.click();
  const dialog = page.getByRole("dialog", { name: questions.titled!.title });
  await expect(dialog.getByText("Which suspect has", { exact: false })).toBeVisible();
  await capture(page, "play-question-dialog");
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await expect(larger).toBeFocused();

  for (const width of [1024, 768]) {
    await page.setViewportSize({ width, height: 900 });
    await capture(page, `play-question-details-at-${width}`);
  }
});

test("the Tables tab shows the organisers' table descriptions", async ({ page }) => {
  const { accounts, contests, notesHeading } = seed();
  await signIn(page, accounts.alice);
  await openPlay(page, contests.features!.id);

  const tabs = page.getByRole("tablist").filter({ has: page.getByRole("tab", { name: "Story" }) });
  await expect(tabs.getByRole("tab")).toHaveText([
    "Story",
    "Tables",
    "Questions",
    "Notes",
    "Questions to organisers",
    "Table",
  ]);
  await openSideTab(page, "Tables");
  const panel = page.getByRole("tabpanel", { name: "Tables" });
  await expect(panel.getByRole("heading", { level: 2, name: notesHeading })).toBeVisible();
  await expect(panel.getByText("lists everyone questioned", { exact: false })).toBeVisible();
  await capture(page, "play-tables-tab");
  await page.setViewportSize({ width: 1024, height: 900 });
  await capture(page, "play-tables-tab-at-1024");
});

test("the diagram draws the tables and their keys, pans and zooms, and gives focus back", async ({ page }) => {
  const { accounts, contests } = seed();
  await signIn(page, accounts.alice);
  await openPlay(page, contests.features!.id);

  const open = page.getByRole("button", { name: "Open the schema as a diagram" });
  await open.click();
  const dialog = page.getByRole("dialog", { name: "Schema diagram" });
  const canvas = dialog.getByRole("application", { name: "Schema diagram" });
  await expect(canvas).toBeVisible();

  await expect(dialog.getByRole("list", { name: "Relations" }).getByRole("listitem")).toHaveText(
    [
      "sightings (room_id) references rooms (id)",
      "sightings (suspect_id) references suspects (id)",
      "suspects (room_id) references rooms (id)",
    ],
    { useInnerText: true },
  );
  // The drawing itself is hidden from assistive technology; its boxes and
  // arrows are counted on the SVG.
  await expect(canvas.locator("[data-table]")).toHaveCount(4);
  await expect(canvas.locator("[data-edge]")).toHaveCount(3);
  // An arrow is read by where it ends, so none may run through a table on
  // its way: every point between its ends lies outside every box.
  const crossings = await canvas.evaluate((frame) => {
    const boxes = [...frame.querySelectorAll<SVGGElement>("[data-table]")].map((table) => {
      const { e: x, f: y } = table.transform.baseVal.consolidate()!.matrix;
      const { width, height } = table.getBBox();
      return { name: table.dataset.table!, x, y, width, height };
    });
    const found: string[] = [];
    for (const edge of frame.querySelectorAll<SVGGElement>("[data-edge]")) {
      for (const path of edge.querySelectorAll("path")) {
        const length = path.getTotalLength();
        for (let at = 0.1; at < 0.9; at += 0.02) {
          const point = path.getPointAtLength(length * at);
          const inside = boxes.find(
            (box) =>
              point.x > box.x + 2 && point.x < box.x + box.width - 2 && point.y > box.y + 2 && point.y < box.y + box.height - 2,
          );
          if (inside) {
            found.push(`${edge.dataset.edge} passes through ${inside.name}`);
            break;
          }
        }
      }
    }
    return found;
  });
  expect.soft(crossings, "arrows drawn through a table").toEqual([]);
  await capture(page, "play-diagram");

  const suspects = canvas.locator('[data-table="suspects"]');
  const zoom = dialog.getByText(/^\d+%$/);
  const percent = async () => Number((await zoom.textContent())!.replace("%", ""));

  const fitted = await percent();
  await dialog.getByRole("button", { name: "Zoom in" }).click();
  await expect.poll(percent).toBeGreaterThan(fitted);

  const before = (await suspects.boundingBox())!;
  await canvas.focus();
  await page.keyboard.press("Shift+ArrowRight");
  await expect.poll(async () => (await suspects.boundingBox())!.x).not.toBe(before.x);

  const frame = (await canvas.boundingBox())!;
  const moved = (await suspects.boundingBox())!;
  await page.mouse.move(frame.x + frame.width / 2, frame.y + frame.height / 2);
  await page.mouse.down();
  await page.mouse.move(frame.x + frame.width / 2 + 120, frame.y + frame.height / 2 + 40, { steps: 6 });
  await page.mouse.up();
  await expect.poll(async () => Math.round((await suspects.boundingBox())!.x - moved.x)).toBeGreaterThan(60);

  const zoomed = await percent();
  await page.mouse.wheel(0, 400);
  await expect.poll(percent).toBeLessThan(zoomed);
  await page.keyboard.press("0");
  await expect.poll(percent).toBe(fitted);
  await capture(page, "play-diagram-fit");

  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await expect(open).toBeFocused();
});
