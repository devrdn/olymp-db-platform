import { expect, test, type Locator, type Page } from "@playwright/test";

import { readSeed } from "./api";
import { SECRET } from "./seed-features";
import { capture, chooseTheme, openPlay, openSideTab, runQuery, scrollWithin, signIn } from "./support";

/**
 * The contest-wide post-beta behaviour: what a sequential contest withholds,
 * what a closed catalogue hides, the prize places, and the monitoring
 * screen with a roster of a few hundred.
 */

test.use({ viewport: { width: 1600, height: 900 } });

const seed = () => readSeed().features;
const secrets = Object.values(SECRET);

/** Every same-origin response body the page receives from here on. */
function recordResponses(page: Page): { bodies: { url: string; text: string }[] } {
  const seen = { bodies: [] as { url: string; text: string }[] };
  page.on("response", async (response) => {
    if (!response.url().startsWith(process.env.E2E_BASE_URL ?? "")) return;
    const type = response.headers()["content-type"] ?? "";
    if (!/json|html|text|javascript|x-component/.test(type)) return;
    try {
      seen.bodies.push({ url: response.url(), text: await response.text() });
    } catch {
      // A redirect or an aborted stream has no body to read.
    }
  });
  return seen;
}

function leaks(text: string): string[] {
  return secrets.filter((secret) => text.includes(secret));
}

test("a sequential contest shows later questions as numbers only, and sends nothing of them", async ({ page }) => {
  const { accounts, contests, questions } = seed();
  const id = contests.sequential!.id;
  await signIn(page, accounts.dave);
  const seen = recordResponses(page);
  await openPlay(page, id);
  // No table descriptions here, with the catalogue open: no Tables tab.
  await expect(page.getByRole("tab", { name: "Story" })).toBeVisible();
  await expect(page.getByRole("tab", { name: "Tables", exact: true })).toHaveCount(0);
  await openSideTab(page, "Questions");

  const panel = page.getByRole("tabpanel", { name: "Questions" });
  await expect(panel.getByRole("heading", { level: 2, name: questions.first!.title })).toBeVisible();
  const locked = panel.getByRole("list", { name: "Questions not open yet" });
  await expect(locked.getByRole("listitem")).toHaveText([
    "Question 2 — opens after question 1",
    "Question 3 — opens after question 2",
  ]);
  await expect(locked.getByRole("button")).toHaveCount(0);
  await expect(locked.getByRole("textbox")).toHaveCount(0);
  await expect(locked.getByRole("link")).toHaveCount(0);
  await capture(page, "play-sequential-locked");
  await page.setViewportSize({ width: 768, height: 900 });
  await capture(page, "play-sequential-locked-at-768");
  await page.setViewportSize({ width: 1600, height: 900 });

  expect(leaks(await page.content())).toEqual([]);
  for (const body of seen.bodies) {
    expect(leaks(body.text), body.url).toEqual([]);
  }

  const read = await page.request.get(`/api/v1/contests/${id}/play/questions`);
  expect(read.ok()).toBe(true);
  const raw = await read.text();
  expect(leaks(raw)).toEqual([]);
  const list = JSON.parse(raw) as { items: Record<string, unknown>[] };
  expect(list.items).toHaveLength(3);
  expect(list.items[0]).toMatchObject({ locked: false, title: questions.first!.title });
  expect(list.items.slice(1)).toEqual([
    { position: 2, locked: true },
    { position: 3, locked: true },
  ]);

  // The clarification picker offers only what the participant can see.
  await openSideTab(page, "Questions to organisers");
  await expect(page.getByRole("combobox", { name: "About" }).getByRole("option")).toHaveText([
    "The contest in general",
    `Question 1: ${questions.first!.title}`,
  ]);

  // Answering opens the next question in full, and only that one.
  await openSideTab(page, "Questions");
  await panel.getByRole("textbox", { name: "Your answer" }).fill(questions.first!.answer);
  await panel.getByRole("button", { name: "Submit", exact: true }).click();
  await expect(panel.getByRole("status").filter({ hasText: /^Correct!/ })).toBeVisible();
  await expect(panel.getByRole("heading", { level: 2, name: SECRET.title2 })).toBeVisible();
  await expect(locked.getByRole("listitem")).toHaveText(["Question 3 — opens after question 2"]);
  const after = await page.content();
  expect([SECRET.title3, SECRET.body3, SECRET.choice3].filter((secret) => after.includes(secret))).toEqual([]);
  for (const body of seen.bodies) {
    expect(
      [SECRET.title3, SECRET.body3, SECRET.choice3].filter((secret) => body.text.includes(secret)),
      body.url,
    ).toEqual([]);
  }
  await capture(page, "play-sequential-second-open");
});

test("a closed catalogue hides the schema and the diagram; no descriptions, no Tables tab; downloads off, no copy", async ({
  page,
}) => {
  const { accounts, contests } = seed();
  const id = contests.hidden!.id;
  await signIn(page, accounts.erin);
  await openPlay(page, id);

  await expect(page.getByRole("region", { name: "Schema" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Open the schema as a diagram" })).toHaveCount(0);
  await expect(page.getByRole("tab", { name: "Story" })).toBeVisible();
  await expect(page.getByRole("tab", { name: "Tables", exact: true })).toHaveCount(0);

  await runQuery(page, "SELECT id, name FROM suspects ORDER BY id");
  await expect(page.getByRole("table").filter({ hasText: "Mallory Kent" }).getByRole("row")).toHaveCount(4);
  await expect(page.getByRole("button", { name: /^Copy (first 5|all) rows$/ })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Download CSV" })).toHaveCount(0);

  // The server withholds the schema, whatever the screen shows.
  const schema = await page.request.get(`/api/v1/contests/${id}/play/schema`);
  expect(schema.status()).toBe(403);
  expect(((await schema.json()) as { error: { code: string } }).error.code).toBe("schema_hidden");
  await capture(page, "play-hidden-catalogue");
});

/** One standings row, found by the participant's name. */
function standingsRow(table: Locator, name: string): Locator {
  return table.getByRole("row").filter({ hasText: name });
}

async function expectPrize(row: Locator, prize: string, mark: string, medal: string | null) {
  const cell = row.getByRole("cell").first();
  await expect(cell).toHaveAccessibleName(prize);
  await expect(cell).toContainText(mark);
  const badge = cell.getByTestId("prize");
  if (medal) await expect(badge).toHaveAttribute("data-medal", medal);
  else await expect(badge).not.toHaveAttribute("data-medal", /./);
}

test("the standings give medals I, II and III to the configured places and a dash to the rest", async ({ page }) => {
  const { accounts, contests } = seed();
  await signIn(page, readSeed().admin);

  await page.goto(`/contests/${contests.features!.id}/standings`);
  const table = page.getByRole("table", { name: "Standings" });
  await expectPrize(standingsRow(table, accounts.bob.login), "First prize", "I", "gold");
  await expectPrize(standingsRow(table, accounts.alice.login), "Second prize", "II", "silver");
  await expectPrize(standingsRow(table, accounts.carol.login), "Third prize", "III", "bronze");
  await expectPrize(standingsRow(table, accounts.dave.login), "No prize", "—", null);
  await capture(page, "staff-standings-prizes");
  await page.setViewportSize({ width: 768, height: 900 });
  await capture(page, "staff-standings-prizes-at-768");
  await page.setViewportSize({ width: 1600, height: 900 });

  await page.goto(`/contests/${contests.hidden!.id}/standings`);
  const winner = standingsRow(table, accounts.frank.login);
  await expect(winner).toContainText("Winner");
  await expectPrize(winner, "First prize", "I", "gold");
  await expectPrize(standingsRow(table, accounts.erin.login), "No prize", "—", null);
  await capture(page, "staff-standings-winner");
});

test("a participant's own table shows the same prize places", async ({ page }) => {
  const { accounts, contests } = seed();
  await signIn(page, accounts.dave);
  await openPlay(page, contests.features!.id);
  await openSideTab(page, "Table");

  const table = page.getByRole("tabpanel", { name: "Table" }).getByRole("table");
  await expectPrize(standingsRow(table, accounts.bob.login), "First prize", "I", "gold");
  await expectPrize(standingsRow(table, accounts.alice.login), "Second prize", "II", "silver");
  await expectPrize(standingsRow(table, accounts.carol.login), "Third prize", "III", "bronze");
  const own = standingsRow(table, accounts.dave.login);
  await expect(own).toContainText("You");
  await expectPrize(own, "No prize", "—", null);
  await capture(page, "play-standings-prizes");
});

test("the monitoring screen opens on a roster of three hundred and stays responsive", async ({ page }) => {
  const { contests, roster } = seed();
  await page.addInitScript(() => {
    const tasks: number[] = [];
    Object.assign(window, { longTasks: tasks });
    new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) tasks.push(entry.duration);
    }).observe({ type: "longtask", buffered: true });
  });
  await signIn(page, readSeed().admin);
  const opened = Date.now();
  await page.goto(`/contests/${contests.monitor!.id}/monitor`);
  const table = page.getByRole("table", { name: "Participants" });
  await expect(table).toHaveAttribute("aria-rowcount", String(roster.length + 1));
  const openMs = Date.now() - opened;
  await expect(page.getByText(`${roster.length} of ${roster.length}`)).toBeVisible();
  // Only the rows in view are rendered.
  expect(await table.getByRole("row").count()).toBeLessThan(80);
  await capture(page, "staff-monitor-roster");

  const firstRow = () => table.getByRole("row").nth(1);
  await expect(firstRow()).toContainText("Roster 001");
  const sorted = Date.now();
  await table.getByRole("button", { name: "Participant" }).click();
  await expect(firstRow()).toContainText("Roster 300");
  const sortMs = Date.now() - sorted;
  await table.getByRole("button", { name: "Participant" }).click();
  await expect(firstRow()).toContainText("Roster 001");

  // Keyboard: from the last heading into the rows, and down past the window.
  await table.getByRole("button", { name: "Last seen" }).focus();
  await page.keyboard.press("Tab");
  await expect(table.getByRole("link", { name: "Roster 001 Ash" })).toBeFocused();
  for (let step = 0; step < 40; step++) await page.keyboard.press("Tab");
  const focused = table.getByRole("link", { name: /^Roster 041 / });
  await expect(focused).toBeFocused();
  await expect(focused).toBeInViewport();

  // Scrolled far away, the focused row stays focused, and Tab goes on from it.
  await scrollWithin(focused, 54 * 250);
  await expect(table.getByRole("link", { name: /^Roster 25\d / }).first()).toBeVisible();
  await expect(focused).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(table.getByRole("link", { name: /^Roster 042 / })).toBeFocused();

  // The theme switches without a reload and the table keeps its scroll.
  const scrolled = await scrollWithin(firstRow(), 54 * 120);
  await page.evaluate(() => Object.assign(window, { stillTheSamePage: true }));
  const html = page.locator("html");
  const dark = (await html.getAttribute("data-theme")) === "dark";
  await chooseTheme(page, dark ? "light" : "dark");
  expect(await page.evaluate(() => (window as { stillTheSamePage?: boolean }).stillTheSamePage)).toBe(true);
  expect(await scrollWithin(firstRow())).toBe(scrolled);

  // A search runs over the whole roster.
  await page.getByRole("searchbox", { name: "Search" }).fill("Roster 29");
  await expect(page.getByText(`10 of ${roster.length}`)).toBeVisible();

  const longest = Math.max(0, ...(await page.evaluate(() => (window as { longTasks?: number[] }).longTasks ?? [])));
  test.info().annotations.push({
    type: "timing",
    description: `open ${openMs} ms, sort ${sortMs} ms, longest main-thread task ${Math.round(longest)} ms`,
  });
  expect(sortMs).toBeLessThan(1_500);
  await page.setViewportSize({ width: 768, height: 900 });
  await capture(page, "staff-monitor-roster-at-768");
});
