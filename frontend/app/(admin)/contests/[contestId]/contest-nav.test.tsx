import { render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";

const usePathname = vi.hoisted(() => vi.fn());
vi.mock("next/navigation", () => ({ usePathname }));

import { ContestNav, type NavGroup } from "./contest-nav";

const ID = "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";

const groups: NavGroup[] = [
  { items: [{ href: `/contests/${ID}`, label: "Overview", exact: true }] },
  {
    label: "Content",
    items: [
      { href: `/contests/${ID}/story`, label: "Story" },
      { href: `/contests/${ID}/questions`, label: "Questions" },
    ],
  },
];

describe("ContestNav", () => {
  test("marks the section being read", () => {
    usePathname.mockReturnValue(`/contests/${ID}/story`);
    render(<ContestNav groups={groups} />);

    expect(screen.getByRole("link", { name: "Story" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Overview" })).not.toHaveAttribute("aria-current");
  });

  /** The overview's address is a prefix of every section's. */
  test("does not mark the overview while a section under it is open", () => {
    usePathname.mockReturnValue(`/contests/${ID}/questions`);
    render(<ContestNav groups={groups} />);

    expect(screen.getByRole("link", { name: "Overview" })).not.toHaveAttribute("aria-current");
    expect(screen.getByRole("link", { name: "Questions" })).toHaveAttribute("aria-current", "page");
  });

  test("marks the overview on its own address", () => {
    usePathname.mockReturnValue(`/contests/${ID}`);
    render(<ContestNav groups={groups} />);

    expect(screen.getByRole("link", { name: "Overview" })).toHaveAttribute("aria-current", "page");
  });

  /** A section stays marked on screens under it. */
  test("keeps a section marked while a screen under it is open", () => {
    usePathname.mockReturnValue(`/contests/${ID}/questions/9f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6`);
    render(<ContestNav groups={groups} />);

    expect(screen.getByRole("link", { name: "Questions" })).toHaveAttribute("aria-current", "page");
  });

  /** `/questions-archive` is not inside `/questions`. */
  test("does not mark a section on an address that merely shares its prefix", () => {
    usePathname.mockReturnValue(`/contests/${ID}/questions-archive`);
    render(<ContestNav groups={groups} />);

    expect(screen.getByRole("link", { name: "Questions" })).not.toHaveAttribute("aria-current");
  });
});

/** Blocking work is marked beside the section that owns it. */
describe("ContestNav, what a section still owes", () => {
  test("names the outstanding work beside the section", () => {
    usePathname.mockReturnValue(`/contests/${ID}`);
    render(
      <ContestNav
        groups={[
          {
            items: [{ href: `/contests/${ID}/questions`, label: "Questions", note: "3" }],
          },
        ]}
      />,
    );

    expect(screen.getByRole("link", { name: /Questions/ })).toHaveTextContent("3");
  });

  test("says nothing about a section that owes nothing", () => {
    usePathname.mockReturnValue(`/contests/${ID}`);
    render(<ContestNav groups={groups} />);

    expect(screen.getByRole("link", { name: "Story" })).toHaveTextContent(/^Story$/);
  });
});
