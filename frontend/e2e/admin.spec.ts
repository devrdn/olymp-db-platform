import { expect, test } from "@playwright/test";

import { readSeed } from "./api";
import { signIn } from "./support";

test("an administrator signs in and sees the contest list", async ({ page }) => {
  const seed = readSeed();

  await signIn(page, seed.admin);

  await expect(page).toHaveURL(/\/contests$/);
  await expect(page.getByRole("heading", { level: 1, name: "Contests" })).toBeVisible();
  // The register is read from the API, so the contest the seed made is proof
  // the page reached it and not only that it rendered.
  await expect(page.getByRole("table").getByRole("link", { name: seed.contest.title })).toBeVisible();
});
