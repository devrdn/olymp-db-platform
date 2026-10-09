import { render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { PARTICIPANT_TABS, ParticipantTabs, tabFromParam, tabHref } from "./participant-tabs";
import { CONTEST, REG } from "./test-fixtures";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

describe("which tab an address names", () => {
  test("reads each tab from ?tab=, and anything else as the timeline", () => {
    for (const tab of PARTICIPANT_TABS) expect(tabFromParam(tab)).toBe(tab);
    expect(tabFromParam(undefined)).toBe("timeline");
    expect(tabFromParam("nonsense")).toBe("timeline");
    expect(tabFromParam(["queries", "answers"])).toBe("queries");
  });

  test("gives each tab an address to share, the timeline without a parameter", () => {
    const base = `/contests/${CONTEST}/monitor/${REG}`;
    expect(tabHref(CONTEST, REG, "timeline")).toBe(base);
    expect(tabHref(CONTEST, REG, "queries")).toBe(`${base}?tab=queries`);
    expect(tabHref(CONTEST, REG, "sessions")).toBe(`${base}?tab=sessions`);
  });
});

describe("the tab strip", () => {
  test("links every tab and marks the current one", () => {
    render(<ParticipantTabs contestId={CONTEST} registrationId={REG} current="answers" dict={dict} />);
    const names = dict.workspace.monitor.participant.tabs;

    const answers = screen.getByRole("link", { name: names.answers });
    expect(answers).toHaveAttribute("aria-current", "page");
    expect(answers).toHaveAttribute("href", `/contests/${CONTEST}/monitor/${REG}?tab=answers`);
    expect(screen.getByRole("link", { name: names.timeline })).not.toHaveAttribute("aria-current");
    expect(screen.getAllByRole("link")).toHaveLength(PARTICIPANT_TABS.length);
  });

  /** Five labels do not fit a phone; the strip scrolls, not the page. */
  test("scrolls sideways inside itself", () => {
    render(<ParticipantTabs contestId={CONTEST} registrationId={REG} current="timeline" dict={dict} />);
    const strip = screen.getByRole("navigation", { name: dict.workspace.monitor.participant.tabsLabel });
    expect(strip.className).toMatch(/(^|\s)overflow-x-auto(\s|$)/);
  });
});
