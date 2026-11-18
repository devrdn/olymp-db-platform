import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";

import { cuedEdges, measureScroller, scrollRowTo } from "@/components/ui/scroll-cue-test-support";

import { TabStrip } from "./tab-strip";

const tabs = [
  { href: "/report", label: "Result" },
  { href: "/report?tab=queries", label: "My queries" },
  { href: "/report?tab=notes", label: "My notes" },
];

afterEach(() => vi.restoreAllMocks());

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

  test("fades the edge the remaining tabs are hidden past, until the strip reaches it", () => {
    measureScroller({ scrollWidth: 524, clientWidth: 339 });
    render(<TabStrip label="What you did" tabs={tabs} current="/report" />);
    const strip = screen.getByRole("navigation");

    expect(strip).toHaveClass("scroll-cue");
    expect(cuedEdges(strip)).toEqual(["end"]);

    scrollRowTo(strip, 185);
    expect(cuedEdges(strip)).toEqual(["start"]);
  });
});
