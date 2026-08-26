import { render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";

const usePathname = vi.hoisted(() => vi.fn());
vi.mock("next/navigation", () => ({ usePathname }));

import { ContestTabs } from "./contest-tabs";

const ID = "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";
const tabs = [
  { href: `/contests/${ID}`, label: "Overview", exact: true as const },
  { href: `/contests/${ID}/story`, label: "Story" },
  { href: `/contests/${ID}/questions`, label: "Questions" },
];

describe("ContestTabs", () => {
  test("marks the section being read", () => {
    usePathname.mockReturnValue(`/contests/${ID}/story`);
    render(<ContestTabs tabs={tabs} />);

    expect(screen.getByRole("link", { name: "Story" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Overview" })).not.toHaveAttribute("aria-current");
  });

  /**
   * The overview owns the workspace's bare address, which is a prefix of every
   * other section's. Matched by prefix it would be marked current on all of
   * them at once, which is the same as marking none.
   */
  test("does not mark the overview while a section under it is open", () => {
    usePathname.mockReturnValue(`/contests/${ID}/questions`);
    render(<ContestTabs tabs={tabs} />);

    expect(screen.getByRole("link", { name: "Overview" })).not.toHaveAttribute("aria-current");
    expect(screen.getByRole("link", { name: "Questions" })).toHaveAttribute("aria-current", "page");
  });

  test("marks the overview on its own address", () => {
    usePathname.mockReturnValue(`/contests/${ID}`);
    render(<ContestTabs tabs={tabs} />);

    expect(screen.getByRole("link", { name: "Overview" })).toHaveAttribute("aria-current", "page");
  });

  /**
   * A question's own screen lives under the question list. Its tab stays
   * marked while it is open, or the author loses their place in the workspace
   * the moment they open anything.
   */
  test("keeps a section marked while a screen under it is open", () => {
    usePathname.mockReturnValue(`/contests/${ID}/questions/9f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6`);
    render(<ContestTabs tabs={tabs} />);

    expect(screen.getByRole("link", { name: "Questions" })).toHaveAttribute("aria-current", "page");
  });

  /**
   * A sibling whose address merely starts with the same characters is not a
   * child. `/questions-archive` is not inside `/questions`.
   */
  test("does not mark a section on an address that merely shares its prefix", () => {
    usePathname.mockReturnValue(`/contests/${ID}/questions-archive`);
    render(<ContestTabs tabs={tabs} />);

    expect(screen.getByRole("link", { name: "Questions" })).not.toHaveAttribute("aria-current");
  });
});
