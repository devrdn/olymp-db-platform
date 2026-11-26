import { expect } from "@playwright/test";

import { readSeed } from "./api";
import { signIn, test } from "./support";

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

  // Inside main: Next's route announcer is an alert of its own, empty here.
  await expect(page.getByRole("main").getByRole("alert")).toContainText("Wrong login or password");
  await expect(page.getByLabel("Login")).toHaveValue(seed.admin.login);
  await expect(page.getByLabel("Password")).toHaveValue("");
  // A sign-in that works clears the failure this attempt counted.
  await signIn(page, seed.admin);
});

test.describe("without JavaScript", () => {
  test.use({ javaScriptEnabled: false });

  test("an administrator signs in through the form's native POST", async ({ page }) => {
    const seed = readSeed();
    // Without a native POST the browser would send the form back to /login
    // as a GET, the password in its address.
    const posted = page.waitForRequest(
      (request) =>
        request.isNavigationRequest() && request.method() === "POST" && new URL(request.url()).pathname === "/login",
    );

    await signIn(page, seed.admin);
    await posted;

    await expect(page).toHaveURL(/\/contests$/);
    // The list streams in behind a skeleton that only a script swaps out, so
    // the session is proven by asking the API who this browser is.
    const me = await page.request.get("/api/v1/auth/me");
    expect(me.status()).toBe(200);
    expect(((await me.json()) as { login: string }).login).toBe(seed.admin.login);
  });
});
