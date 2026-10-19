import { expect, test, type Page } from "@playwright/test";

import { readSeed } from "./api";
import { runQuery, signIn } from "./support";

/**
 * The participant's main path in a read_write contest, in order, on one
 * page: each step starts from where the previous one left the screen. The
 * answer comes last because answering the only question closes the console:
 * with nothing left to work towards, a run or a reset would be refused.
 */
test("a participant runs a query, resets their database and answers", async ({ page }) => {
  const seed = readSeed();

  await test.step("signs in and opens the running contest", async () => {
    await signIn(page, seed.participant);
    await expect(page).toHaveURL(/\/my$/);

    const row = page.getByRole("row").filter({ hasText: seed.contest.title });
    await row.getByRole("link", { name: "Open", exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`/contests/${seed.contest.id}/play$`));
    await expect(page.getByRole("button", { name: "Run", exact: true })).toBeVisible();
  });

  await test.step("runs a query and sees its rows", async () => {
    await runQuery(page, "SELECT name, alibi FROM suspects ORDER BY id");

    const rows = resultTable(page).getByRole("row");
    // The header and the game script's three suspects.
    await expect(rows).toHaveCount(4);
    await expect(rows.nth(2)).toContainText("Mallory Kent");
  });

  await test.step("changes the database, resets it, and sees the result cleared", async () => {
    await runQuery(page, "DELETE FROM suspects WHERE id = 2");
    const changed = page.getByRole("status").filter({ hasText: "1 rows changed." });
    await expect(changed).toBeVisible();

    await page.getByRole("button", { name: "Reset database" }).click();
    const dialog = page.getByRole("dialog", { name: "Reset your database?" });
    await dialog.getByRole("button", { name: "Reset", exact: true }).click();
    await expect(
      dialog.getByRole("status").filter({ hasText: "Your database is back to how the contest started it." }),
    ).toBeVisible();
    // Two controls are named Close once the reset is done: the corner one and
    // the footer's; the footer's comes last.
    await dialog.getByRole("button", { name: "Close" }).last().click();
    await expect(dialog).toBeHidden();

    await expect(page.getByText("Run a query to see its result here.")).toBeVisible();
    await expect(changed).toHaveCount(0);
  });

  await test.step("finds the deleted row back after the reset", async () => {
    await runQuery(page, "SELECT name, alibi FROM suspects ORDER BY id");
    const rows = resultTable(page).getByRole("row");
    await expect(rows).toHaveCount(4);
    await expect(rows.nth(2)).toContainText("Mallory Kent");
  });

  await test.step("submits answers and sees each judged", async () => {
    await page.getByRole("tab", { name: "Questions" }).click();
    const answer = page.getByRole("textbox", { name: "Your answer" });
    const submit = page.getByRole("button", { name: "Submit", exact: true });

    await answer.fill(seed.question.wrongAnswer);
    await submit.click();
    await expect(page.getByRole("status").filter({ hasText: "Not correct." })).toBeVisible();

    await answer.fill(seed.question.answer);
    await submit.click();
    await expect(page.getByRole("status").filter({ hasText: /^Correct!/ })).toBeVisible();
  });
});

/** The table under the console, told from any other by its alibi column. */
function resultTable(page: Page) {
  return page.getByRole("table").filter({ has: page.getByRole("columnheader", { name: /alibi/i }) });
}
