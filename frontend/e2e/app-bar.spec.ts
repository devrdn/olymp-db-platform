import { expect } from "@playwright/test";

import { readSeed } from "./api";
import { signIn, test } from "./support";

/**
 * The folded app bar in a real browser: a staff bar holds more destinations
 * than fit in a row at a laptop's width, so they sit behind the menu button.
 */

test.use({ viewport: { width: 1280, height: 800 } });

test("the menu takes staff to a destination and is closed on the page it opens", async ({ page }) => {
  const seed = readSeed();
  await signIn(page, seed.admin);
  await expect(page).toHaveURL(/\/contests$/);

  const menu = page.getByRole("button", { name: "Menu", exact: true });
  await expect(menu).toHaveAttribute("aria-expanded", "false");
  const panel = page.locator(`[id="${await menu.getAttribute("aria-controls")}"]`);
  await expect(panel).toBeHidden();

  await menu.click();
  await expect(page.getByRole("button", { name: "Close menu" })).toHaveAttribute("aria-expanded", "true");
  await expect(panel).toBeVisible();
  await expect(panel.getByRole("link", { name: "Contests" })).toHaveAttribute("aria-current", "page");

  await panel.getByRole("link", { name: "Accounts" }).click();

  await expect(page).toHaveURL(/\/users$/);
  const heading = page.getByRole("heading", { level: 1, name: "Accounts" });
  await expect(heading).toBeVisible();
  await expect(menu).toHaveAttribute("aria-expanded", "false");
  // Looked up again: the panel must exist on the new page, and be closed.
  const panelNow = page.locator(`[id="${await menu.getAttribute("aria-controls")}"]`);
  await expect(panelNow).toHaveCount(1);
  await expect(panelNow).toBeHidden();
  // Nothing of the menu, its dim included, lies over the new page.
  expect(
    await heading.evaluate((element) => {
      const box = element.getBoundingClientRect();
      return element.contains(document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2));
    }),
  ).toBe(true);
});
