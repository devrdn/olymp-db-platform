import { expect, type Page } from "@playwright/test";

import type { Account } from "./api";

/** Signs in through the form, as a person would, and waits to leave it. */
export async function signIn(page: Page, account: Account): Promise<void> {
  await page.goto("/login");
  await page.getByLabel("Login").fill(account.login);
  await page.getByLabel("Password").fill(account.password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).not.toHaveURL(/\/login(\?|$)/);
}

/**
 * Replaces the console's text with `sql` and runs it; the caller asserts what
 * came back. The editor is a plain textarea until CodeMirror has loaded and
 * takes its place; waiting for the editable element keeps the text from
 * landing in the one about to go.
 */
export async function runQuery(page: Page, sql: string): Promise<void> {
  const editor = page.getByRole("textbox", { name: "Your query" });
  await expect(editor).toHaveAttribute("contenteditable", "true");
  await editor.click();
  await page.keyboard.press("ControlOrMeta+a");
  await page.keyboard.insertText(sql);
  await expect(editor).toHaveText(sql);

  await page.getByRole("button", { name: "Run", exact: true }).click();
}
