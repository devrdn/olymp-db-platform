import { expect, test, type Locator } from "@playwright/test";

import { Api, readSeed } from "./api";
import { capture, openPlay, openSideTab, signIn } from "./support";

/**
 * The bonus question, last of the post-beta checks because it ends the
 * features contest: Alice's correct bonus gives back the points her wrong
 * attempt cost, in the standings and on her report once the contest is over.
 */

test.use({ viewport: { width: 1600, height: 900 } });

function pointsOf(table: Locator, login: string): Locator {
  return table.getByRole("row").filter({ hasText: login }).getByRole("cell").last();
}

test("a correct bonus answer gives back the penalty, in the standings and on the participant's report", async ({
  page,
  browser,
}) => {
  const seed = readSeed();
  const { accounts, contests, questions } = seed.features;
  const id = contests.features!.id;
  await signIn(page, accounts.alice);
  await openPlay(page, id);

  await openSideTab(page, "Table");
  const own = page.getByRole("tabpanel", { name: "Table" }).getByRole("table");
  await expect(pointsOf(own, accounts.alice.login)).toHaveText("7");

  await openSideTab(page, "Questions");
  const panel = page.getByRole("tabpanel", { name: "Questions" });
  // The bonus is the second question, the only one titled "The locked room".
  const bonusTitle = panel.getByRole("heading", { level: 2, name: questions.bonus!.title });
  await expect(bonusTitle).toBeVisible();
  await expect(panel.getByText("up to 5 pts back")).toBeVisible();
  // At the side column's default width the whole tag must show.
  await expect.soft(panel.getByText("up to 5 pts back"), "the bonus tag is cut off").toBeInViewport({ ratio: 1 });
  const answers = panel.getByRole("textbox", { name: "Your answer" });
  await answers.first().fill("Library");
  await panel.getByRole("button", { name: "Submit", exact: true }).first().click();
  await expect(panel.getByRole("status").filter({ hasText: "Not correct." })).toBeVisible();
  await answers.first().fill(questions.bonus!.answer);
  await panel.getByRole("button", { name: "Submit", exact: true }).first().click();
  await expect(
    panel.getByRole("status").filter({ hasText: "Correct! Up to 5 lost points are given back." }),
  ).toBeVisible();
  await capture(page, "play-bonus-correct");

  // Seven points and three lost to the penalty; the bonus of five gives the
  // three back and no more. The wrong bonus attempt cost nothing.
  await openSideTab(page, "Table");
  await expect(pointsOf(own, accounts.alice.login)).toHaveText("10", { timeout: 20_000 });
  await capture(page, "play-bonus-standings");

  const staff = await browser.newContext({
    baseURL: process.env.E2E_BASE_URL,
    ignoreHTTPSErrors: process.env.E2E_IGNORE_HTTPS_ERRORS === "1",
    locale: "en-US",
    viewport: { width: 1600, height: 900 },
  });
  const desk = await staff.newPage();
  await signIn(desk, seed.admin);
  await desk.goto(`/contests/${id}/standings`);
  const table = desk.getByRole("table", { name: "Standings" });
  const alice = table.getByRole("row").filter({ hasText: accounts.alice.login });
  await expect(alice.getByRole("cell").nth(3)).toHaveText("10");
  await expect(alice.getByRole("cell").first()).toHaveAccessibleName("Second prize");
  await staff.close();

  // The report opens once the contest is over for the participant.
  const operator = await Api.open();
  try {
    await operator.signIn(seed.admin.login, seed.admin.password);
    await operator.call("POST", `/contests/${id}/status`, { status: "finished" });
  } finally {
    await operator.close();
  }
  await page.goto(`/profile/contests/${id}`);
  // The report names questions by number: the second is the bonus.
  const report = page.getByRole("table", { name: "By question" });
  const row = (cell: string) => report.getByRole("row").filter({ has: page.getByRole("cell", { name: cell, exact: true }) });
  await expect(row("2 bonus").getByRole("cell").last()).toHaveText("3");
  await expect(row("1").getByRole("cell").last()).toHaveText("7");
  const result = page.getByRole("region", { name: "Your result" });
  await expect(result.getByRole("definition").first()).toHaveText("10");
  await expect(page.getByText("A bonus row shows the lost points it gave back", { exact: false })).toBeVisible();
  await capture(page, "profile-report-bonus");
  await page.setViewportSize({ width: 768, height: 900 });
  await capture(page, "profile-report-bonus-at-768");
});
