import { expect, type Locator, type Page } from "@playwright/test";

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
  // Focused rather than clicked: a completion popup left open by the last
  // query can lie over the editor's text.
  await editor.focus();
  await page.keyboard.press("ControlOrMeta+a");
  await page.keyboard.insertText(sql);
  await expect(editor).toHaveText(sql);

  await page.getByRole("button", { name: "Run", exact: true }).click();
}

/** Opens a participant's play screen and waits for the console to be usable. */
export async function openPlay(page: Page, contestId: string): Promise<void> {
  await page.goto(`/contests/${contestId}/play`);
  await expect(page.getByRole("textbox", { name: "Your query" })).toHaveAttribute("contenteditable", "true");
}

/** The editor's text with its line breaks, as the browser lays it out. */
export async function editorText(page: Page): Promise<string> {
  return page.getByRole("textbox", { name: "Your query" }).evaluate((editor) => (editor as HTMLElement).innerText);
}

/** The side column's tab, scrolled into its strip and chosen. */
export async function openSideTab(page: Page, name: string | RegExp): Promise<void> {
  const tab = page.getByRole("tab", { name, exact: typeof name === "string" });
  await tab.click();
  await expect(tab).toHaveAttribute("aria-selected", "true");
}

type Theme = "light" | "dark";

/** Presses the app bar's theme toggle until the page wears `theme`. */
export async function chooseTheme(page: Page, theme: Theme): Promise<void> {
  const html = page.locator("html");
  for (let press = 0; press < 3; press++) {
    if ((await html.getAttribute("data-theme")) === theme) return;
    await page.getByRole("button", { name: /^(Switch to the (light|dark) theme|Follow the system theme)$/ }).first().click();
  }
  await expect(html).toHaveAttribute("data-theme", theme);
}

/**
 * Saves a screenshot of the page in each theme under E2E_SCREENS_DIR, for a
 * person to look at; does nothing when the variable is not set. The theme is
 * set on the page directly, since a modal dialog makes the app bar's toggle
 * unreachable, and put back afterwards.
 */
export async function capture(page: Page, name: string, themes: Theme[] = ["light", "dark"]): Promise<void> {
  const dir = process.env.E2E_SCREENS_DIR;
  if (!dir) return;
  // A play screen's clock gets its first sync about a second after load.
  const clock = page.getByRole("timer");
  if ((await clock.count()) > 0) {
    await expect(clock).toHaveText(/\d:\d\d/, { timeout: 5_000 }).catch(() => undefined);
  }
  const html = page.locator("html");
  const was = await html.getAttribute("data-theme");
  for (const theme of themes) {
    // data-theme-swap holds transitions off, as the toggle does.
    await html.evaluate((root, value) => {
      root.setAttribute("data-theme-swap", "");
      root.setAttribute("data-theme", value);
    }, theme);
    await page.screenshot({ path: `${dir}/${name}-${theme}.png`, animations: "disabled" });
  }
  await html.evaluate((root, value) => {
    if (value === null) root.removeAttribute("data-theme");
    else root.setAttribute("data-theme", value);
    root.removeAttribute("data-theme-swap");
  }, was);
}

/**
 * The scroll offset of the nearest box around `inner` that scrolls; with
 * `top`, scrolls it there first.
 */
export async function scrollWithin(inner: Locator, top?: number): Promise<number> {
  return inner.evaluate((element, to) => {
    let box: HTMLElement | null = element as HTMLElement;
    while (box && !(box.scrollHeight > box.clientHeight + 1 && /auto|scroll/.test(getComputedStyle(box).overflowY))) {
      box = box.parentElement;
    }
    if (!box) throw new Error("nothing around this element scrolls");
    if (to !== undefined) box.scrollTop = to;
    return box.scrollTop;
  }, top);
}

/** Opens a fresh SQL tab and returns its name, for `closeSqlTab`. */
export async function newSqlTab(page: Page): Promise<string> {
  const tabs = page.getByRole("tablist", { name: "SQL tabs" }).getByRole("tab");
  const count = await tabs.count();
  await page.getByRole("button", { name: "New tab" }).click();
  await expect(tabs).toHaveCount(count + 1);
  const tab = tabs.last();
  await expect(tab).toHaveAttribute("aria-selected", "true");
  // The tab's text ends with its close mark.
  return (await tab.textContent())!.replace(/[^\p{L}\p{N}]+$/u, "").trim();
}

/** Closes a SQL tab, confirming the loss of its text. */
export async function closeSqlTab(page: Page, name: string): Promise<void> {
  page.once("dialog", (dialog) => void dialog.accept());
  await page.getByRole("button", { name: `Close ${name}` }).click();
  await expect(page.getByRole("tablist", { name: "SQL tabs" }).getByRole("tab", { name })).toHaveCount(0);
}
