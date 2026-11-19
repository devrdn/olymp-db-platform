import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";

import { cuedEdges, measureScroller } from "@/components/ui/scroll-cue-test-support";

const usePathname = vi.hoisted(() => vi.fn());
vi.mock("next/navigation", () => ({ usePathname }));

import { ContestNav, type NavGroup } from "./contest-nav";

const ID = "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";
const LABEL = "Secțiunile olimpiadei";

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
  test("is named in the reader's language by the label it is given", () => {
    usePathname.mockReturnValue(`/contests/${ID}`);
    render(<ContestNav groups={groups} label={LABEL} />);

    expect(screen.getByRole("navigation", { name: LABEL })).toBeInTheDocument();
  });

  test("marks the section being read", () => {
    usePathname.mockReturnValue(`/contests/${ID}/story`);
    render(<ContestNav groups={groups} label={LABEL} />);

    expect(screen.getByRole("link", { name: "Story" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Overview" })).not.toHaveAttribute("aria-current");
  });

  /** The overview's address is a prefix of every section's. */
  test("does not mark the overview while a section under it is open", () => {
    usePathname.mockReturnValue(`/contests/${ID}/questions`);
    render(<ContestNav groups={groups} label={LABEL} />);

    expect(screen.getByRole("link", { name: "Overview" })).not.toHaveAttribute("aria-current");
    expect(screen.getByRole("link", { name: "Questions" })).toHaveAttribute("aria-current", "page");
  });

  test("marks the overview on its own address", () => {
    usePathname.mockReturnValue(`/contests/${ID}`);
    render(<ContestNav groups={groups} label={LABEL} />);

    expect(screen.getByRole("link", { name: "Overview" })).toHaveAttribute("aria-current", "page");
  });

  /** A section stays marked on screens under it. */
  test("keeps a section marked while a screen under it is open", () => {
    usePathname.mockReturnValue(`/contests/${ID}/questions/9f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6`);
    render(<ContestNav groups={groups} label={LABEL} />);

    expect(screen.getByRole("link", { name: "Questions" })).toHaveAttribute("aria-current", "page");
  });

  /** `/questions-archive` is not inside `/questions`. */
  test("does not mark a section on an address that merely shares its prefix", () => {
    usePathname.mockReturnValue(`/contests/${ID}/questions-archive`);
    render(<ContestNav groups={groups} label={LABEL} />);

    expect(screen.getByRole("link", { name: "Questions" })).not.toHaveAttribute("aria-current");
  });
});

/** Blocking work is marked beside the section that owns it. */
describe("ContestNav, what a section still owes", () => {
  test("names the outstanding work beside the section", () => {
    usePathname.mockReturnValue(`/contests/${ID}`);
    render(
      <ContestNav
        label={LABEL}
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
    render(<ContestNav groups={groups} label={LABEL} />);

    expect(screen.getByRole("link", { name: "Story" })).toHaveTextContent(/^Story$/);
  });
});

describe("ContestNav, as a row below the wide breakpoint", () => {
  afterEach(() => vi.restoreAllMocks());

  // Ten sections in a 339px row: without a cue the last six are simply absent.
  test("fades the edge the remaining sections are hidden past", () => {
    usePathname.mockReturnValue(`/contests/${ID}`);
    measureScroller({ scrollWidth: 924, clientWidth: 339 });
    render(<ContestNav groups={groups} label={LABEL} />);

    const row = screen.getByRole("navigation");
    // Only as a row: the column never scrolls, and a mask would clip its focus ring.
    expect(row).toHaveClass("max-wide:scroll-cue");
    expect(row).not.toHaveClass("scroll-cue");
    expect(cuedEdges(row)).toEqual(["end"]);
  });
});

describe("ContestNav, where the row and the column switch", () => {
  test("switches at wide, not at narrow, so a tablet keeps the whole width for the work", () => {
    usePathname.mockReturnValue(`/contests/${ID}`);
    render(<ContestNav groups={groups} label={LABEL} />);

    const nav = screen.getByRole("navigation");
    expect(nav).toHaveClass("max-wide:flex-row");
    expect(screen.getByText("Content")).toHaveClass("max-wide:hidden");
    expect(screen.getByRole("link", { name: "Story" })).toHaveClass("max-wide:border-b-2");
    expect(nav.outerHTML).not.toContain("max-narrow:");
  });
});

describe("ContestNav, the notes in the row", () => {
  test("keeps what a section owes visible beside its tab and in its accessible name", () => {
    usePathname.mockReturnValue(`/contests/${ID}`);
    render(
      <ContestNav
        label={LABEL}
        groups={[
          {
            items: [
              { href: `/contests/${ID}/story`, label: "Story", note: "none" },
              { href: `/contests/${ID}/questions`, label: "Questions", note: "3" },
            ],
          },
        ]}
      />,
    );

    const link = screen.getByRole("link", { name: "Questions 3" });
    const note = screen.getByText("3");
    expect(link).toContainElement(note);
    expect(note.className).not.toMatch(/hidden/);
    expect(note).toHaveClass("text-warn");
    expect(screen.getByRole("link", { name: "Story none" })).toBeInTheDocument();
  });
});
