import { randomBytes } from "node:crypto";

import { expect, test, type Browser, type Page } from "@playwright/test";

import { readSeed, type Account } from "./api";
import { capture, openPlay, openSideTab, signIn } from "./support";

/**
 * Questions to the organisers, end to end across three browsers: Alice asks,
 * the organiser answers from the desk, Bob reads what was published. Each
 * participant's tab is opened only when the check needs it, so the unread
 * marker has something to mark.
 */

const TAB = "Questions to organisers";

async function open(browser: Browser, account: Account, path: string): Promise<Page> {
  const context = await browser.newContext({
    baseURL: process.env.E2E_BASE_URL,
    ignoreHTTPSErrors: process.env.E2E_IGNORE_HTTPS_ERRORS === "1",
    locale: "en-US",
    viewport: { width: 1600, height: 900 },
  });
  const page = await context.newPage();
  await signIn(page, account);
  await page.goto(path);
  return page;
}

/** The tab with the unread dot reads "… (new)"; without it, just its name. */
function clarificationsTab(page: Page, unread: boolean) {
  return page.getByRole("tab", { name: unread ? `${TAB} (new)` : TAB, exact: true });
}

/** Opens the marked tab; opening it is what clears the mark. */
async function openUnread(page: Page): Promise<void> {
  await clarificationsTab(page, true).click();
  await expect(clarificationsTab(page, false)).toHaveAttribute("aria-selected", "true");
}

test("a question asked by one participant is answered publicly, privately, and an announcement reaches both", async ({
  browser,
}) => {
  test.setTimeout(180_000);
  const { accounts, contests } = readSeed().features;
  const admin = readSeed().admin;
  const id = contests.features!.id;
  const tag = randomBytes(3).toString("hex");
  const publicQuestion = `Is the Study the same room as the library annex? (${tag})`;
  const publicAnswer = `No: the annex is part of the **Library**. (${tag})`;
  const privateQuestion = `May I use window functions? (${tag})`;
  const privateAnswer = `Yes, any SELECT is allowed. (${tag})`;
  const announcement = `The sightings table uses local time. (${tag})`;

  const alice = await open(browser, accounts.alice, `/contests/${id}/play`);
  const bob = await open(browser, accounts.bob, `/contests/${id}/play`);
  const staff = await open(browser, admin, `/contests/${id}/clarifications`);
  await expect(alice.getByRole("button", { name: "Run", exact: true })).toBeVisible();
  await expect(bob.getByRole("button", { name: "Run", exact: true })).toBeVisible();
  await expect(clarificationsTab(bob, false)).toBeVisible();

  await test.step("Alice asks about a question; it waits for an answer", async () => {
    await openSideTab(alice, TAB);
    await alice.getByRole("combobox", { name: "About" }).selectOption({ label: "Question 1: The alibi" });
    await alice.getByRole("textbox", { name: "Your question" }).fill(publicQuestion);
    await alice.getByRole("button", { name: "Send question" }).click();
    await expect(alice.getByRole("status").filter({ hasText: "Your question was sent." })).toBeVisible();
    const mine = alice.getByRole("article").filter({ hasText: publicQuestion });
    await expect(mine).toContainText("Your question");
    await expect(mine).toContainText("About question 1: The alibi");
    await expect(mine).toContainText("Waiting for an answer");
    await capture(alice, "play-clarifications-asked");
  });

  await test.step("the organiser sees who asked and answers for everyone", async () => {
    // The contest's navigation counts the questions waiting.
    await staff.reload();
    await expect(staff.getByRole("link", { name: /^Participant questions\s*1$/ })).toBeVisible();
    await expect(staff.getByRole("button", { name: "Waiting (1)" })).toHaveAttribute("aria-pressed", "true");
    const item = staff.getByRole("article").filter({ hasText: publicQuestion });
    await expect(item).toContainText(`Alice Ashford (${accounts.alice.login})`, { timeout: 20_000 });
    await expect(item).toContainText("About question 1: The alibi");
    await capture(staff, "staff-desk-waiting");
    await item.getByRole("textbox", { name: "Answer" }).fill(publicAnswer);
    await item.getByRole("radio", { name: "Every participant" }).check();
    await item.getByRole("button", { name: "Send answer" }).click();
    await expect(staff.getByRole("status").filter({ hasText: "Answer sent." })).toBeVisible();
  });

  await test.step("Bob is told there is something new, and reads the answer without learning who asked", async () => {
    await expect(clarificationsTab(bob, true)).toBeVisible({ timeout: 20_000 });
    await capture(bob, "play-clarifications-unread");
    // The dot must be where a sighted participant can see it, not scrolled
    // out of the tab strip.
    await expect.soft(bob.getByTestId("clarifications-unread")).toBeInViewport();
    await openUnread(bob);
    const seen = bob.getByRole("article").filter({ hasText: publicQuestion });
    await expect(seen).toContainText("Answer for everyone");
    // The organisers' Markdown renders: the bold word is a strong element.
    await expect(seen.getByText("Library", { exact: true })).toBeVisible();
    await expect(seen).toContainText("No: the annex is part of the Library.");
    await expect(seen).not.toContainText("Your question");
    const page = await bob.content();
    expect(page).not.toContain(accounts.alice.login);
    expect(page).not.toContain("Alice Ashford");
    const feed = await bob.request.get(`/api/v1/contests/${id}/play/clarifications`);
    const raw = await feed.text();
    expect(raw).toContain(publicQuestion);
    expect(raw).not.toContain(accounts.alice.login);
    expect(raw).not.toContain(accounts.alice.id);
    await capture(bob, "play-clarifications-public-answer");

    // The marker stays cleared after a reload: the last item seen is kept.
    await openPlay(bob, id);
    await expect(clarificationsTab(bob, false)).toBeVisible();
    await bob.waitForTimeout(2_000);
    await expect(clarificationsTab(bob, true)).toHaveCount(0);
  });

  await test.step("Alice sees her own question published", async () => {
    const mine = alice.getByRole("article").filter({ hasText: publicQuestion });
    await expect(mine).toContainText("Published for everyone", { timeout: 20_000 });
    await expect(mine).toContainText("No: the annex is part of the Library.");
  });

  await test.step("a private answer reaches only the participant who asked", async () => {
    await openSideTab(bob, "Story");
    await alice.getByRole("combobox", { name: "About" }).selectOption({ label: "The contest in general" });
    await alice.getByRole("textbox", { name: "Your question" }).fill(privateQuestion);
    await alice.getByRole("textbox", { name: "Your question" }).press("ControlOrMeta+Enter");
    await expect(alice.getByRole("article").filter({ hasText: privateQuestion })).toContainText("Waiting for an answer");

    const item = staff.getByRole("article").filter({ hasText: privateQuestion });
    await expect(item).toBeVisible({ timeout: 20_000 });
    await expect(item).toContainText("About the contest in general");
    await item.getByRole("textbox", { name: "Answer" }).fill(privateAnswer);
    await expect(item.getByRole("radio", { name: "Only the participant who asked" })).toBeChecked();
    await item.getByRole("button", { name: "Send answer" }).click();
    await expect(staff.getByRole("status").filter({ hasText: "Answer sent." })).toBeVisible();

    await expect(alice.getByRole("article").filter({ hasText: privateQuestion })).toContainText(privateAnswer, {
      timeout: 20_000,
    });

    // Bob gets no marker, and neither the question nor the answer.
    await bob.waitForTimeout(5_000);
    await expect(clarificationsTab(bob, true)).toHaveCount(0);
    const feed = await (await bob.request.get(`/api/v1/contests/${id}/play/clarifications`)).text();
    expect(feed).not.toContain(privateQuestion);
    expect(feed).not.toContain(privateAnswer);
    await openSideTab(bob, TAB);
    await expect(bob.getByText(privateQuestion)).toHaveCount(0);
  });

  await test.step("an announcement reaches both participants", async () => {
    await openSideTab(alice, "Story");
    await openSideTab(bob, "Story");
    await staff.getByRole("textbox", { name: "New announcement" }).fill(announcement);
    await staff.getByRole("button", { name: "Post announcement" }).click();
    await expect(staff.getByRole("status").filter({ hasText: "Announcement posted." })).toBeVisible();
    await capture(staff, "staff-desk-answered");

    for (const page of [alice, bob]) {
      await expect(clarificationsTab(page, true)).toBeVisible({ timeout: 20_000 });
      await openUnread(page);
      const section = page.getByRole("region", { name: "Announcements" });
      await expect(section).toContainText(announcement);
    }
    await capture(bob, "play-clarifications-announcement");
    await bob.setViewportSize({ width: 1024, height: 900 });
    await capture(bob, "play-clarifications-announcement-at-1024");
  });

  await test.step("the desk lists both answers as answered", async () => {
    await expect(staff.getByRole("button", { name: "Waiting (0)" })).toBeVisible();
    // With nothing waiting, the navigation no longer counts a question.
    await expect.soft(staff.getByRole("link", { name: "Participant questions", exact: true })).toBeVisible();
    await staff.getByRole("button", { name: /^Answered \(\d+\)$/ }).click();
    await expect(staff.getByRole("article").filter({ hasText: publicQuestion })).toContainText("public");
    await expect(staff.getByRole("article").filter({ hasText: privateQuestion })).toContainText("private");
    await staff.setViewportSize({ width: 768, height: 900 });
    await capture(staff, "staff-desk-answered-at-768");
  });

  for (const page of [alice, bob, staff]) await page.context().close();
});
