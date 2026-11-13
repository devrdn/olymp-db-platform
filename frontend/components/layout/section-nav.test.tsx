import { render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";

vi.mock("next/navigation", () => ({ usePathname: () => "/contests/c-1/story" }));

import { SectionNav } from "./section-nav";

const items = [
  { href: "/contests", label: "Contests" },
  { href: "/audit", label: "Audit" },
];

describe("SectionNav", () => {
  test("keeps the section marked while the visitor is deeper inside it", () => {
    render(<SectionNav items={items} />);

    expect(screen.getByRole("link", { name: "Contests" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Audit" })).not.toHaveAttribute("aria-current");
  });

  test("carries no glyph beside the words", () => {
    // SPEC §2, rule 1: words only, no icons.
    const { container } = render(<SectionNav items={items} />);

    expect(container.querySelectorAll("svg")).toHaveLength(0);
    expect(screen.getByRole("link", { name: "Contests" })).toHaveAccessibleName("Contests");
  });

  test("every destination is a link in one nav landmark, reachable by Tab", () => {
    render(<SectionNav items={items} />);

    const nav = screen.getByRole("navigation");
    expect(nav.querySelectorAll("a")).toHaveLength(items.length);
    for (const link of screen.getAllByRole("link")) expect(link).not.toHaveAttribute("tabindex", "-1");
  });

  test("scrolls a focused link fully into view inside its scroller", async () => {
    const scrollIntoView = vi.fn();
    Element.prototype.scrollIntoView = scrollIntoView;
    render(<SectionNav items={items} />);

    screen.getByRole("link", { name: "Audit" }).focus();

    expect(scrollIntoView).toHaveBeenCalledWith({ block: "nearest", inline: "nearest" });
    // Leaves room for the 2px ring and its 2px offset on both sides of the link.
    expect(screen.getByRole("link", { name: "Audit" }).className).toContain("max-narrow:scroll-mx-1.5");
  });
});
