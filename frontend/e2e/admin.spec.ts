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

test("a refused sign-in clears the password and keeps the login", async ({ page }) => {
  const seed = readSeed();

  await page.goto("/login");
  await page.getByLabel("Login").fill(seed.admin.login);
  await page.getByLabel("Password").fill(`${seed.admin.password}-wrong`);
  await page.getByRole("button", { name: "Sign in" }).click();

  await expect(page.getByRole("alert")).toContainText("Wrong login or password");
  await expect(page.getByLabel("Login")).toHaveValue(seed.admin.login);
  await expect(page.getByLabel("Password")).toHaveValue("");
  // A sign-in that works clears the failure this attempt counted.
  await signIn(page, seed.admin);
});
