import { readFile } from "node:fs/promises";

import { expect, test, type Locator, type Page } from "@playwright/test";

import { Api, readSeed } from "./api";
import { capture, signIn } from "./support";

/**
 * The results export on the features contest once it is over. An organiser
 * downloads the prize winners as CSV on a desktop, and the page is measured at
 * a phone's width, a small laptop's and a wide screen's: no sideways scroll,
 * the download button in reach without scrolling to the end, and the list's
 * last row clear of it.
 */

// features-results ends the contest, and this name sorts after it; run on its
// own, this file ends it the same way. The preview reads the staff table,
// which is cached for a few seconds, so the page opens only once it says final.
test.beforeAll(async () => {
  const seed = readSeed();
  const id = seed.features.contests.features.id;
  const operator = await Api.open();
  const status = async () => (await operator.call<{ status: string }>("GET", `/contests/${id}/results`)).status;
  try {
    await operator.signIn(seed.admin.login, seed.admin.password);
    if ((await status()) !== "final") {
      await operator.call("POST", `/contests/${id}/status`, { status: "finished" });
      await expect.poll(status, { timeout: 15_000 }).toBe("final");
    }
  } finally {
    await operator.close();
  }
});

const HEADER =
  "place,prize,registration_id,full_name,login,disqualified,solved,penalty,points,last_solved_at,bonus_credit,status";

function exportUrl(): string {
  return `/contests/${readSeed().features.contests.features.id}/standings/export`;
}

/** The download button, whatever count or format its label carries. */
function downloadButton(page: Page): Locator {
  return page.getByRole("button", { name: /^Download (CSV|PDF|protocol) · \d+$/ });
}

/** The participant list's rows: the list items that carry a checkbox. */
function participantRows(page: Page): Locator {
  return page.getByRole("listitem").filter({ has: page.getByRole("checkbox") });
}

async function openExport(page: Page): Promise<void> {
  await signIn(page, readSeed().admin);
  await page.goto(exportUrl());
  await expect(page.getByRole("heading", { name: "Export the results" })).toBeVisible();
  await expect(participantRows(page)).toHaveCount(4);
}

async function horizontalOverflow(page: Page): Promise<{ scrollWidth: number; clientWidth: number }> {
  return page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
  }));
}

/** Scrolls the page to its end and says how far that was. */
async function scrollToEnd(page: Page): Promise<number> {
  return page.evaluate(() => {
    const root = document.scrollingElement ?? document.documentElement;
    root.scrollTop = root.scrollHeight;
    return root.scrollTop;
  });
}

/**
 * The strip the download button sits in: the button's own parent, which the
 * form pins to the viewport's bottom while it is one column. Its top is where
 * the list must stop.
 */
async function barBox(button: Locator): Promise<{ top: number; bottom: number }> {
  return button.evaluate((element) => {
    const box = element.parentElement!.getBoundingClientRect();
    return { top: box.top, bottom: box.bottom };
  });
}

/**
 * Whether the row's top and bottom edges are the row itself to a pointer, not
 * something laid over them.
 */
async function rowUncovered(row: Locator): Promise<boolean> {
  return row.evaluate((element) => {
    const box = element.getBoundingClientRect();
    const x = box.left + Math.min(24, box.width / 2);
    return [box.top + 2, box.bottom - 2].every((y) => {
      const hit = document.elementFromPoint(x, y);
      return hit !== null && element.contains(hit);
    });
  });
}

type Measured = {
  width: number;
  scrollWidth: number;
  clientWidth: number;
  scrolled: number;
  bar: { top: number; bottom: number };
  lastRowBottom: number;
  gap: number;
};

/**
 * The three things a one-column export must keep, asserted and returned for
 * the log: no sideways scroll, the download button in the viewport both at
 * the top of the page and at its end, and the last row above the button's
 * strip, uncovered, at full scroll.
 */
async function checkOneColumn(page: Page): Promise<Measured> {
  const width = page.viewportSize()!.width;
  const overflow = await horizontalOverflow(page);
  expect(overflow.scrollWidth, "the page scrolls sideways").toBeLessThanOrEqual(overflow.clientWidth);

  const button = downloadButton(page);
  await page.evaluate(() => window.scrollTo(0, 0));
  await expect(button, "the download button is out of reach at the top of the page").toBeInViewport({ ratio: 1 });

  const scrolled = await scrollToEnd(page);
  expect(scrolled, "the page is too short to show where the list ends").toBeGreaterThan(0);
  await expect(button, "the download button is out of reach at the end of the list").toBeInViewport({ ratio: 1 });

  const bar = await barBox(button);
  const last = participantRows(page).last();
  const lastBox = (await last.boundingBox())!;
  const lastRowBottom = lastBox.y + lastBox.height;
  expect(lastRowBottom, "the last row ends under the download bar").toBeLessThanOrEqual(bar.top);
  expect(await rowUncovered(last), "something lies over the last row").toBe(true);

  return {
    width,
    ...overflow,
    scrolled,
    bar,
    lastRowBottom: Math.round(lastRowBottom),
    gap: Math.round(bar.top - lastRowBottom),
  };
}

test("an organiser exports the prize winners as CSV from the standings", async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 900 });
  const { accounts, contests } = readSeed().features;
  await signIn(page, readSeed().admin);
  await page.goto(`/contests/${contests.features.id}/standings`);
  await page.getByRole("link", { name: "Export", exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`${exportUrl()}$`));
  await expect(page.getByRole("heading", { name: "Export the results" })).toBeVisible();

  const alice = participantRows(page).filter({ hasText: accounts.alice.login });
  await expect(alice).toContainText("Second prize");
  await expect(alice).toHaveText(/^\s*2\D/);

  // The contest is over, so the protocol is offered.
  await expect(page.getByRole("radio", { name: "Protocol" })).toBeEnabled();

  await page.getByRole("button", { name: "Prize winners" }).click();
  await expect(page.getByText("3 of 4 ticked")).toBeVisible();
  const lines = await downloadCsv(page, "Download CSV · 3");
  expect(lines[0]).toBe(HEADER);
  expect(lines.slice(1).map((line) => line.split(",")[4])).toEqual([
    accounts.bob.login,
    accounts.alice.login,
    accounts.carol.login,
  ]);
  await capture(page, "results-export");
});

test("the export page fits a phone and keeps the download button in reach", async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 812 });
  await openExport(page);

  const measured = await checkOneColumn(page);
  console.log(`export at 375×812: ${JSON.stringify(measured)}`);
  await capture(page, "results-export-at-375-end");
  await page.evaluate(() => window.scrollTo(0, 0));
  await capture(page, "results-export-at-375");

  // Nobody, then places one to three: the button counts what is ticked, and
  // the file holds those rows and no others.
  const { accounts } = readSeed().features;
  await page.getByRole("button", { name: "None", exact: true }).click();
  await expect(page.getByText("0 of 4 ticked")).toBeVisible();
  await expect(downloadButton(page)).toBeDisabled();
  await expect(page.getByText("Tick at least one participant")).toBeVisible();

  await page.getByLabel("Places from").fill("1");
  await page.getByLabel("to", { exact: true }).fill("3");
  await page.getByRole("button", { name: "Tick", exact: true }).click();
  await expect(page.getByText("3 of 4 ticked")).toBeVisible();

  const lines = await downloadCsv(page, "Download CSV · 3");
  expect(lines[0]).toBe(HEADER);
  expect(lines).toHaveLength(4);
  const rows = lines.slice(1).map((line) => line.split(","));
  expect(rows.map((cells) => cells[0])).toEqual(["1", "2", "3"]);
  expect(rows.map((cells) => cells[4])).toEqual([accounts.bob.login, accounts.alice.login, accounts.carol.login]);
});

test("the export page keeps the download button in reach on a small laptop", async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 768 });
  await openExport(page);
  const measured = await checkOneColumn(page);
  console.log(`export at 1024×768: ${JSON.stringify(measured)}`);
  await capture(page, "results-export-at-1024-end");
  await page.evaluate(() => window.scrollTo(0, 0));
  await capture(page, "results-export-at-1024");
});

test("the export page sets the options beside the list on a wide screen", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openExport(page);

  const overflow = await horizontalOverflow(page);
  expect(overflow.scrollWidth, "the page scrolls sideways").toBeLessThanOrEqual(overflow.clientWidth);

  const options = (await page.getByRole("group", { name: "Format" }).boundingBox())!;
  const list = (await page.getByRole("heading", { name: "Whom" }).boundingBox())!;
  const row = (await participantRows(page).first().boundingBox())!;
  expect(options.x, "the options are not beside the list").toBeGreaterThanOrEqual(row.x + row.width);
  // Side by side, the two columns start on the same line rather than one under the other.
  expect(Math.abs(options.y - list.y), "the options start below the list").toBeLessThan(80);
  // The button belongs to the options column, not to a bar across the page.
  const button = (await downloadButton(page).boundingBox())!;
  expect(button.x, "the download button left the options column").toBeGreaterThanOrEqual(options.x);
  expect(button.y, "the download button is not under the options").toBeGreaterThan(options.y);

  console.log(
    `export at 1440×900: ${JSON.stringify({
      ...overflow,
      list: { left: Math.round(row.x), right: Math.round(row.x + row.width), top: Math.round(list.y) },
      options: { left: Math.round(options.x), top: Math.round(options.y) },
    })}`,
  );
  await capture(page, "results-export-at-1440");
});

/** Presses the download button by its label and returns the file's lines. */
async function downloadCsv(page: Page, label: string): Promise<string[]> {
  const [download] = await Promise.all([
    page.waitForEvent("download"),
    page.getByRole("button", { name: label, exact: true }).click(),
  ]);
  expect(download.suggestedFilename()).toMatch(/^results-[0-9a-f-]+-\d{8}T\d{6}Z\.csv$/);
  const text = await readFile((await download.path())!, "utf8");
  // CRLF after every record, the last included.
  expect(text.endsWith("\r\n"), "the file does not end with CRLF").toBe(true);
  return text.slice(0, -2).split("\r\n");
}
