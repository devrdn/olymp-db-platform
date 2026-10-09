import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import { TabStrip } from "./tab-strip";

const tabs = [
  { href: "/report", label: "Result" },
  { href: "/report?tab=queries", label: "My queries" },
  { href: "/report?tab=notes", label: "My notes" },
];

describe("a strip of tabs", () => {
  test("names the group, links every tab and marks the one in the address", () => {
    render(<TabStrip label="What you did" tabs={tabs} current="/report?tab=queries" />);

    expect(screen.getByRole("navigation", { name: "What you did" })).toBeInTheDocument();
    expect(screen.getAllByRole("link")).toHaveLength(3);
    expect(screen.getByRole("link", { name: "My queries" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Result" })).not.toHaveAttribute("aria-current");
    expect(screen.getByRole("link", { name: "My notes" })).toHaveAttribute("href", "/report?tab=notes");
  });

  /** Five labels do not fit a phone; the page must never scroll sideways at 375px. */
  test("scrolls sideways inside itself rather than widening the page", () => {
    render(<TabStrip label="What you did" tabs={tabs} current="/report" />);

    expect(screen.getByRole("navigation")).toHaveClass("overflow-x-auto");
  });
});
