import { expect, test, type Locator, type Page } from "@playwright/test";

import { Api, readSeed, type Account } from "./api";
import { SECRET } from "./seed-features";
import { capture, cellUnder, chooseTheme, openPlay, openSideTab, runQuery, scrollWithin, signIn } from "./support";

/**
 * The contest-wide post-beta behaviour: what a sequential contest withholds,
 * what a closed catalogue hides, the prize places, the monitoring screen
 * with a roster of a few hundred, a settings save after every kind of
 * content save, a content edit refused over another session's write,
 * consecutive content saves, an edited form keeping the version its draft was
 * based on, and a confirmation asked inside another dialog.
 */

test.use({ viewport: { width: 1600, height: 900 } });

const seed = () => readSeed().features;
const secrets = Object.values(SECRET);

/**
 * Every same-origin response body the page receives from here on. `bodies`
 * waits for the ones still being read, so a check never runs over half of
 * them.
 */
function recordResponses(page: Page): { bodies: () => Promise<{ url: string; text: string }[]> } {
  const origin = test.info().project.use.baseURL;
  if (!origin) throw new Error("no baseURL: same-origin responses cannot be told from the rest");
  const read: Promise<{ url: string; text: string } | null>[] = [];
  page.on("response", (response) => {
    if (!response.url().startsWith(origin)) return;
    const type = response.headers()["content-type"] ?? "";
    // The events stream never ends, so its body would never be read; it
    // carries no content either (ARCHITECTURE.md section 8).
    if (!/json|html|text|javascript|x-component/.test(type) || type.includes("event-stream")) return;
    read.push(
      response.text().then(
        (text) => ({ url: response.url(), text }),
        // A redirect or an aborted stream has no body to read.
        () => null,
      ),
    );
  });
  return {
    bodies: async () => (await Promise.all(read)).filter((body): body is { url: string; text: string } => body !== null),
  };
}

function leaks(text: string): string[] {
  return secrets.filter((secret) => text.includes(secret));
}

test("a sequential contest shows later questions as numbers only, and sends nothing of them", async ({ page }) => {
  const { accounts, contests, questions } = seed();
  const id = contests.sequential.id;
  await signIn(page, accounts.dave);
  const seen = recordResponses(page);
  await openPlay(page, id);
  // No table descriptions here, with the catalogue open: no Tables tab.
  await expect(page.getByRole("tab", { name: "Story" })).toBeVisible();
  await expect(page.getByRole("tab", { name: "Tables", exact: true })).toHaveCount(0);
  await openSideTab(page, "Questions");

  const panel = page.getByRole("tabpanel", { name: "Questions" });
  await expect(panel.getByRole("heading", { level: 2, name: questions.first.title })).toBeVisible();
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
  const received = await seen.bodies();
  // The play page renders the questions on the server, so its document is
  // what carries the list; it must be among what was checked.
  expect(received.some((body) => new URL(body.url).pathname === `/contests/${id}/play`)).toBe(true);
  for (const body of received) {
    expect(leaks(body.text), body.url).toEqual([]);
  }

  const read = await page.request.get(`/api/v1/contests/${id}/play/questions`);
  expect(read.ok()).toBe(true);
  const raw = await read.text();
  expect(leaks(raw)).toEqual([]);
  const list = JSON.parse(raw) as { items: Record<string, unknown>[] };
  expect(list.items).toHaveLength(3);
  expect(list.items[0]).toMatchObject({ locked: false, title: questions.first.title });
  expect(list.items.slice(1)).toEqual([
    { position: 2, locked: true },
    { position: 3, locked: true },
  ]);

  // The clarification picker offers only what the participant can see.
  await openSideTab(page, "Organisers");
  await expect(page.getByRole("combobox", { name: "About" }).getByRole("option")).toHaveText([
    "The contest in general",
    `Question 1: ${questions.first.title}`,
  ]);

  // Answering opens the next question in full, and only that one.
  await openSideTab(page, "Questions");
  await panel.getByRole("textbox", { name: "Your answer" }).fill(questions.first.answer);
  await panel.getByRole("button", { name: "Submit", exact: true }).click();
  await expect(panel.getByRole("status").filter({ hasText: /^Correct!/ })).toBeVisible();
  await expect(panel.getByRole("heading", { level: 2, name: SECRET.title2 })).toBeVisible();
  await expect(locked.getByRole("listitem")).toHaveText(["Question 3 — opens after question 2"]);
  const after = await page.content();
  expect([SECRET.title3, SECRET.body3, SECRET.choice3].filter((secret) => after.includes(secret))).toEqual([]);
  for (const body of await seen.bodies()) {
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
  const id = contests.hidden.id;
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

/** The name a participant's row carries in the play tab, which labels rows by full name. */
function nameOf(account: Account): string {
  if (!account.fullName) throw new Error(`the seed recorded no full name for ${account.login}`);
  return account.fullName;
}

/** One standings row, found by the participant's name. */
function standingsRow(table: Locator, name: string): Locator {
  return table.getByRole("row").filter({ hasText: name });
}

/**
 * The organiser's table has a Prize column of its own, on every row. The play
 * tab's panel variant folds the medal into the Place column and draws it only
 * for a row that holds a prize.
 */
async function expectPrize(
  table: Locator,
  row: Locator,
  prize: string,
  mark: string,
  medal: string | null,
  column: "Prize" | "Place" = "Prize",
) {
  const cell = await cellUnder(table, row, column);
  if (column === "Place" && !medal) {
    // The place must be drawn, or a missing row would pass the count below.
    await expect(cell.getByTestId("place")).toBeVisible();
    await expect(cell.getByTestId("prize")).toHaveCount(0);
    return;
  }
  await expect(cell).toContainText(prize);
  await expect(cell).toContainText(mark);
  const badge = cell.getByTestId("prize");
  if (medal) await expect(badge).toHaveAttribute("data-medal", medal);
  else await expect(badge).not.toHaveAttribute("data-medal", /./);
}

test("the standings give medals I, II and III to the configured places and a dash to the rest", async ({ page }) => {
  const { accounts, contests } = seed();
  await signIn(page, readSeed().admin);

  await page.goto(`/contests/${contests.features.id}/standings`);
  const table = page.getByRole("table", { name: "Standings" });
  await expectPrize(table, standingsRow(table, accounts.bob.login), "First prize", "I", "gold");
  await expectPrize(table, standingsRow(table, accounts.alice.login), "Second prize", "II", "silver");
  await expectPrize(table, standingsRow(table, accounts.carol.login), "Third prize", "III", "bronze");
  await expectPrize(table, standingsRow(table, accounts.dave.login), "No prize", "—", null);
  await capture(page, "staff-standings-prizes");
  await page.setViewportSize({ width: 768, height: 900 });
  await capture(page, "staff-standings-prizes-at-768");
  await page.setViewportSize({ width: 1600, height: 900 });

  await page.goto(`/contests/${contests.hidden.id}/standings`);
  const winner = standingsRow(table, accounts.frank.login);
  await expect(winner).toContainText("Winner");
  await expectPrize(table, winner, "First prize", "I", "gold");
  await expectPrize(table, standingsRow(table, accounts.erin.login), "No prize", "—", null);
  await capture(page, "staff-standings-winner");
});

test("a participant's own table shows the same prize places", async ({ page }) => {
  const { accounts, contests } = seed();
  await signIn(page, accounts.dave);
  await openPlay(page, contests.features.id);
  await openSideTab(page, "Table");

  const table = page.getByRole("tabpanel", { name: "Table" }).getByRole("table");
  await expectPrize(table, standingsRow(table, nameOf(accounts.bob)), "First prize", "I", "gold", "Place");
  await expectPrize(table, standingsRow(table, nameOf(accounts.alice)), "Second prize", "II", "silver", "Place");
  await expectPrize(table, standingsRow(table, nameOf(accounts.carol)), "Third prize", "III", "bronze", "Place");
  const own = standingsRow(table, nameOf(accounts.dave));
  await expect(own).toContainText("You");
  await expectPrize(table, own, "No prize", "—", null, "Place");
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
  await page.goto(`/contests/${contests.monitor.id}/monitor`);
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

const SAVED = "Saved.";
const CONTEST_CHANGED = "This contest changed while you were deciding";

/**
 * One of the settings screen's panels, by its heading. A panel is an unnamed
 * section, so it is found by the heading it holds; what is pressed and read
 * inside it is found by role.
 */
function panel(page: Page, heading: string): Locator {
  const title = page.getByRole("heading", { level: 3, name: heading, exact: true });
  // Every section around the panel holds its heading too; the panel is the
  // innermost, which comes last in document order.
  return page.locator("section").filter({ has: title }).last();
}

/** The contest's own fields end in the access panel, where their save sits. */
function contestFields(page: Page): Locator {
  return panel(page, "Who may enter, and from where");
}

/** The answer to the next server action the page sends, which may be asked for only after a confirmation. */
function actionAnswered(page: Page): Promise<unknown> {
  return page.waitForResponse(
    (response) => response.request().method() === "POST" && response.request().headers()["next-action"] !== undefined,
  );
}

/**
 * Presses a form's save and returns once its server action has settled. The
 * button reads its label again only then, so an outcome left on screen by an
 * earlier save is never taken for this one's.
 */
async function press(button: Locator): Promise<void> {
  const page = button.page();
  const label = ((await button.textContent()) ?? "").trim();
  const answered = actionAnswered(page);
  await button.click();
  // The answer has arrived, so the action is pending; its body streams on,
  // and only the action's settling gives the button its label back.
  await answered;
  await expect(button).toHaveText(label);
  await expect(button).toBeEnabled();
}

/** Saves one form with its save button, "Save" unless named, and expects "Saved.", never a refusal. */
async function savePanel(form: Locator, save = "Save"): Promise<void> {
  await press(form.getByRole("button", { name: save, exact: true }));
  await expect(form.getByRole("alert")).toHaveCount(0);
  await expect(form.getByRole("status").filter({ hasText: SAVED })).toBeVisible();
}

/** Saves the contest's settings and expects "Saved.", never a refusal. */
async function saveSettings(page: Page): Promise<void> {
  await savePanel(contestFields(page));
}

/** Runs `work` as the organiser in an API session of its own, beside the page's. */
async function elsewhere<T>(work: (other: Api) => Promise<T>): Promise<T> {
  const { organiser } = seed().accounts;
  const other = await Api.open();
  try {
    await other.signIn(organiser.login, organiser.password);
    return await work(other);
  } finally {
    await other.close();
  }
}

/** Writes the contest's story from a session of its own, so the page's copy goes stale. */
async function changeElsewhere(contestId: string, story = "Changed from another session."): Promise<void> {
  await elsewhere((other) => other.call("PUT", `/contests/${contestId}/story`, { translations: { en: story } }));
}

/** The story as the API holds it now, in English. */
async function storedStory(contestId: string): Promise<string> {
  const story = await elsewhere((other) =>
    other.call<{ translations: Record<string, string> }>("GET", `/contests/${contestId}/story`),
  );
  return (story.translations.en ?? "").trim();
}

/** The contest's version as the API holds it now. */
async function storedVersion(contestId: string): Promise<number> {
  return (await elsewhere((other) => other.call<{ version: number }>("GET", `/contests/${contestId}`))).version;
}

/**
 * The story's form on the story screen. It has no name of its own, so it is
 * the form that holds the story's save; the table descriptions below it are
 * a form of their own.
 */
function storyForm(page: Page): Locator {
  return page.locator("form").filter({ has: page.getByRole("button", { name: "Save the story", exact: true }) });
}

/** The table descriptions' region on the story screen. */
function tableDescriptions(page: Page): Locator {
  return page.getByRole("region", { name: "Table descriptions" });
}

/** The question list's row of the question titled `title`. */
function questionRow(page: Page, title: string): Locator {
  return page.getByRole("row").filter({ has: page.getByRole("link", { name: title, exact: true }) });
}

/** Deletes a question from its row in the list, answering the confirmation; returns once the action has answered. */
async function deleteFromList(row: Locator): Promise<void> {
  const page = row.page();
  await row.getByRole("button", { name: "Delete", exact: true }).click();
  const question = page.getByRole("dialog", { name: "Delete this question and its answers?" });
  const answered = actionAnswered(page);
  await question.getByRole("button", { name: "Delete", exact: true }).click();
  await answered;
  await expect(question).toHaveCount(0);
}

/** Opens the organiser's settings screen; every later screen is reached from it in the same session. */
async function openSettings(page: Page): Promise<string> {
  const { accounts, contests } = seed();
  const id = contests.editing.id;
  await signIn(page, accounts.organiser);
  await page.goto(`/contests/${id}/settings`);
  await expect(page.getByRole("heading", { level: 2, name: "Settings" })).toBeVisible();
  return id;
}

/**
 * A section of the contest through the workspace's own links, so the router's
 * copy of the screen is what shows; `heading` is the screen's own. A link's
 * name can go on with a marker after the section's name ("Story none").
 */
async function goToSection(page: Page, link: string, heading: string): Promise<void> {
  const sections = page.getByRole("navigation", { name: "Contest sections" });
  await sections.getByRole("link", { name: new RegExp(`^${link}(\\s|$)`) }).click();
  await expect(page.getByRole("heading", { level: 2, name: heading, exact: true })).toBeVisible();
}

test.describe("a settings save follows every content save", () => {
  test("the settings save twice in a row", async ({ page }) => {
    await openSettings(page);
    await saveSettings(page);
    await saveSettings(page);
  });

  test("a language save, then the settings", async ({ page }) => {
    await openSettings(page);
    await savePanel(panel(page, "Languages"));
    await saveSettings(page);
  });

  test("a question saved on its own screen, then the settings", async ({ page }) => {
    const id = await openSettings(page);
    const { questions } = seed();
    await goToSection(page, "Questions", "Questions");
    await page.getByRole("link", { name: questions.editing.title }).click();
    await expect(page).toHaveURL(new RegExp(`/contests/${id}/questions/${questions.editing.id}$`));
    await savePanel(page.getByRole("main"));

    await goToSection(page, "Settings", "Settings");
    await saveSettings(page);
  });

  test("the table descriptions, then the settings", async ({ page }) => {
    await openSettings(page);
    await goToSection(page, "Story", "The crime story");
    await savePanel(tableDescriptions(page), "Save the table descriptions");

    await goToSection(page, "Settings", "Settings");
    await saveSettings(page);
  });

  test("a change from another session is refused with a reload, and the reloaded form saves", async ({ page }) => {
    await changeElsewhere(await openSettings(page));

    const fields = contestFields(page);
    await press(fields.getByRole("button", { name: "Save", exact: true }));
    await expect(fields.getByRole("alert")).toContainText(CONTEST_CHANGED);
    await expect(fields.getByRole("status").filter({ hasText: SAVED })).toHaveCount(0);

    const reload = fields.getByRole("button", { name: "Reload the page" });
    await reload.click();
    await expect(fields.getByRole("alert")).toHaveCount(0);
    await expect(reload).toHaveCount(0);
    await saveSettings(page);
  });

  test("a refused save keeps what the organiser typed", async ({ page }) => {
    await openSettings(page);
    const fields = contestFields(page);
    const networks = fields.getByRole("textbox", { name: "Allowed networks" });
    const rate = fields.getByRole("spinbutton", { name: "Queries per minute" });
    await networks.fill("not-a-network");
    await rate.fill("37");
    await expect(networks).toHaveValue("not-a-network");
    await expect(rate).toHaveValue("37");

    await press(fields.getByRole("button", { name: "Save", exact: true }));
    // An ordinary refusal of a value, not the contest having moved.
    await expect(fields.getByRole("alert")).toContainText("Wrong IP range format.");
    await expect(fields.getByRole("alert")).not.toContainText(CONTEST_CHANGED);

    // Both are read, so a failure says which kind of field lost its value.
    await expect.soft(networks).toHaveValue("not-a-network");
    await expect.soft(rate).toHaveValue("37");
  });
});

/**
 * The settings, the prize places and the archive save the one contest row and
 * move its one version, so each save must hand the other two forms the new one.
 */
test.describe("the settings, the prize places and the archive share one version", () => {
  test("the prize places, then the archive, then the settings", async ({ page }) => {
    await openSettings(page);
    await savePanel(panel(page, "Prize places"));
    await savePanel(panel(page, "Archive"));
    await saveSettings(page);
  });

  test("the settings, then the archive, then the prize places", async ({ page }) => {
    await openSettings(page);
    await saveSettings(page);
    await savePanel(panel(page, "Archive"));
    await savePanel(panel(page, "Prize places"));
  });

  for (const heading of ["Prize places", "Archive"]) {
    test(`${heading} over a change from another session is refused with a reload, and the reloaded form saves`, async ({
      page,
    }) => {
      await changeElsewhere(await openSettings(page));

      const form = panel(page, heading);
      await press(form.getByRole("button", { name: "Save", exact: true }));
      await expect(form.getByRole("alert")).toContainText(CONTEST_CHANGED);

      await form.getByRole("button", { name: "Reload the page" }).click();
      await expect(form.getByRole("alert")).toHaveCount(0);
      await savePanel(form);
    });
  }
});

/**
 * The content edits move the same version as the settings, so a screen
 * rendered before another session's write is refused, and every save hands
 * the screen the version it moved to.
 */
test.describe("a content edit is refused over a change from another session", () => {
  test("the story is refused with a reload, and the reloaded story saves the other session's text", async ({
    page,
  }) => {
    const id = await openSettings(page);
    await goToSection(page, "Story", "The crime story");
    const theirs = `Rewritten in another session, run ${Date.now()}.`;
    await changeElsewhere(id, theirs);

    const form = storyForm(page);
    await press(form.getByRole("button", { name: "Save the story", exact: true }));
    await expect(form.getByRole("alert")).toContainText(CONTEST_CHANGED);
    await expect(form.getByRole("status").filter({ hasText: SAVED })).toHaveCount(0);
    expect(await storedStory(id)).toBe(theirs);

    const reload = form.getByRole("button", { name: "Reload the page" });
    await reload.click();
    await expect(form.getByRole("alert")).toHaveCount(0);
    await expect(reload).toHaveCount(0);
    // The form starts over from the page as it is now, so it saves the other
    // session's text rather than writing the old one back over it.
    await expect(form).toContainText(theirs);
    await savePanel(form, "Save the story");
    expect(await storedStory(id)).toBe(theirs);
  });

  test("a question delete from a list another session's question edit has moved is refused with a reload, and the reloaded list deletes it", async ({
    page,
  }) => {
    const id = await openSettings(page);
    const { editing } = seed().questions;
    const spare = `Spare question, run ${Date.now()}`;
    const question = (title: string, body: string) => ({
      is_visible: true,
      kind: "text",
      points: 10,
      texts: { en: { title, body_md: body } },
    });
    await elsewhere((other) =>
      other.call("POST", `/contests/${id}/questions`, question(spare, "Deleted by the browser checks.")),
    );
    await goToSection(page, "Questions", "Questions");
    const row = questionRow(page, spare);
    await expect(row).toBeVisible();

    await elsewhere((other) =>
      other.call("PUT", `/contests/${id}/questions/${editing.id}`, {
        ...question(editing.title, `Who has no alibi? Edited in another session, run ${Date.now()}.`),
        answers: [{ match_kind: "exact_ci", value: editing.answer }],
      }),
    );

    await deleteFromList(row);
    await expect(row.getByRole("alert")).toContainText(CONTEST_CHANGED);
    const reload = row.getByRole("button", { name: "Reload the page" });
    await reload.click();
    await expect(row.getByRole("alert")).toHaveCount(0);
    await expect(reload).toHaveCount(0);
    await expect(row).toBeVisible();
    await expect(questionRow(page, editing.title)).toBeVisible();

    await deleteFromList(row);
    await expect(row).toHaveCount(0);
    await expect(questionRow(page, editing.title)).toBeVisible();
  });
});

test.describe("consecutive saves of untouched content forms in one session each send the version the last one moved to", () => {
  test("the story twice, then the table descriptions on the same screen", async ({ page }) => {
    const id = await openSettings(page);
    await goToSection(page, "Story", "The crime story");
    const before = await storedVersion(id);

    await savePanel(storyForm(page), "Save the story");
    await savePanel(storyForm(page), "Save the story");
    await savePanel(tableDescriptions(page), "Save the table descriptions");
    // Each save moved the version, so a form that kept the one it was
    // rendered at would have been refused above.
    expect(await storedVersion(id)).toBe(before + 3);
  });

  test("a question saved twice on its own screen", async ({ page }) => {
    const id = await openSettings(page);
    const { editing } = seed().questions;
    await goToSection(page, "Questions", "Questions");
    await page.getByRole("link", { name: editing.title, exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`/contests/${id}/questions/${editing.id}$`));
    const before = await storedVersion(id);

    await savePanel(page.getByRole("main"));
    await savePanel(page.getByRole("main"));
    expect(await storedVersion(id)).toBe(before + 2);
  });
});

/**
 * From its first edit a form keeps the version its draft was based on, so a
 * save of another form on the same screen leaves it behind.
 */
test.describe("an edited form keeps the version its draft was based on", () => {
  test("the story edited and not saved, then the table descriptions saved: the story is refused with a reload, and the reloaded story saves", async ({
    page,
  }) => {
    const id = await openSettings(page);
    const saved = `Saved before the story screen opened, run ${Date.now()}.`;
    await changeElsewhere(id, saved);
    await goToSection(page, "Story", "The crime story");

    const form = storyForm(page);
    // The editor has no name of its own; its saved text is where the caret goes.
    await form.getByText(saved, { exact: true }).click();
    await page.keyboard.press("End");
    const typed = " Typed and not yet saved.";
    await page.keyboard.insertText(typed);
    await expect(form).toContainText(`${saved}${typed}`);

    await savePanel(tableDescriptions(page), "Save the table descriptions");

    await press(form.getByRole("button", { name: "Save the story", exact: true }));
    await expect(form.getByRole("alert")).toContainText(CONTEST_CHANGED);
    await expect(form.getByRole("status").filter({ hasText: SAVED })).toHaveCount(0);
    // The draft is kept on the refusal, and none of it landed.
    await expect(form).toContainText(`${saved}${typed}`);
    expect(await storedStory(id)).toBe(saved);

    const reload = form.getByRole("button", { name: "Reload the page" });
    await reload.click();
    await expect(form.getByRole("alert")).toHaveCount(0);
    await expect(reload).toHaveCount(0);
    await expect(form).toContainText(saved);
    await expect(form).not.toContainText(typed.trim());
    await savePanel(form, "Save the story");
    expect(await storedStory(id)).toBe(saved);
  });

  test("the story typed in and saved, then typed in and saved again: each save releases the draft, and both land", async ({
    page,
  }) => {
    const id = await openSettings(page);
    const saved = `Saved before the story screen opened, run ${Date.now()}.`;
    await changeElsewhere(id, saved);
    await goToSection(page, "Story", "The crime story");
    const before = await storedVersion(id);
    const form = storyForm(page);

    // The editor has no name of its own; its saved text is where the caret goes.
    await form.getByText(saved, { exact: true }).click();
    await page.keyboard.press("End");
    await page.keyboard.insertText(" First.");
    await savePanel(form, "Save the story");
    expect(await storedStory(id)).toBe(`${saved} First.`);

    // An edited form that kept its first version after its own save would be
    // refused here, its own save having moved the contest.
    await form.getByText(`${saved} First.`, { exact: true }).click();
    await page.keyboard.press("End");
    await page.keyboard.insertText(" Second.");
    await savePanel(form, "Save the story");
    expect(await storedStory(id)).toBe(`${saved} First. Second.`);
    expect(await storedVersion(id)).toBe(before + 2);
  });
});

test("the roles dialog's own question is answered, and the roles dialog stays usable with focus back in it", async ({
  page,
}) => {
  const { frank } = seed().accounts;
  await signIn(page, readSeed().admin);
  await page.goto(`/users?q=${encodeURIComponent(frank.login)}`);
  await page.getByRole("checkbox", { name: `Select ${nameOf(frank)}` }).check();
  await page.getByRole("button", { name: "Roles", exact: true }).click();

  const roles = page.getByRole("dialog", { name: "Change roles for 1 accounts" });
  await expect(roles).toBeVisible();
  const replace = roles.getByRole("button", { name: "Replace roles" });
  // Nothing ticked: the dialog asks before it strips every role.
  await replace.click();
  const question = page.getByRole("dialog", { name: "Remove every role?" });
  await expect(question).toBeVisible();
  // The corner control shares the answer's name; the answer comes first.
  await question.getByRole("button", { name: "Go back" }).first().click();

  await expect(question).toHaveCount(0);
  await expect(roles).toBeVisible();
  await expect(replace).toBeFocused();

  // Still usable: a box ticks and the dialog submits, with no second question.
  const student = roles.getByRole("checkbox", { name: /^Student/ });
  await student.check();
  await expect(student).toBeChecked();
  await replace.click();
  await expect(roles.getByRole("status")).toContainText("1 changed");
  await expect(question).toHaveCount(0);
  await roles.getByRole("button", { name: "Done" }).click();
  await expect(roles).toHaveCount(0);
});
