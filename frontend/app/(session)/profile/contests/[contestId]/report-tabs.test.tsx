import { render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { REPORT_TABS, ReportTabs, tabFromParam, tabHref } from "./report-tabs";

const CONTEST = "6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d01";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

describe("which tab an address names", () => {
  test("reads each tab from ?tab=, and anything else as the result", () => {
    for (const tab of REPORT_TABS) expect(tabFromParam(tab)).toBe(tab);
    expect(tabFromParam(undefined)).toBe("summary");
    expect(tabFromParam("nonsense")).toBe("summary");
    expect(tabFromParam(["queries", "answers"])).toBe("queries");
  });

  test("gives every tab an address, and the default one no parameter", () => {
    const base = `/profile/contests/${CONTEST}`;
    expect(tabHref(CONTEST, "summary")).toBe(base);
    expect(tabHref(CONTEST, "queries")).toBe(`${base}?tab=queries`);
    expect(tabHref(CONTEST, "notes")).toBe(`${base}?tab=notes`);
  });
});

describe("the strip of tabs", () => {
  test("marks the tab in the address and links to every other", () => {
    render(<ReportTabs contestId={CONTEST} current="answers" t={dict.profile.report} />);

    const names = dict.profile.report.tabs;
    expect(screen.getByRole("navigation", { name: dict.profile.report.tabsLabel })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: names.answers })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: names.summary })).not.toHaveAttribute("aria-current");
    expect(screen.getByRole("link", { name: names.queries })).toHaveAttribute(
      "href",
      `/profile/contests/${CONTEST}?tab=queries`,
    );
  });
});
