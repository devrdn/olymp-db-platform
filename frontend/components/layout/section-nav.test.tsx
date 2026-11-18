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

  test("scrolls a focused link fully into view inside the menu, which scrolls", () => {
    const scrollIntoView = vi.fn();
    Element.prototype.scrollIntoView = scrollIntoView;
    render(<SectionNav items={items} layout="menu" />);

    screen.getByRole("link", { name: "Audit" }).focus();

    expect(scrollIntoView).toHaveBeenCalledWith({ block: "nearest", inline: "nearest" });
  });

  test("leaves focus alone in the row, which has nothing to scroll", () => {
    const scrollIntoView = vi.fn();
    Element.prototype.scrollIntoView = scrollIntoView;
    render(<SectionNav items={items} />);

    screen.getByRole("link", { name: "Audit" }).focus();

    expect(scrollIntoView).not.toHaveBeenCalled();
  });

  test("lays the same destinations out as rows a finger can hit in the menu", () => {
    render(<SectionNav items={items} layout="menu" />);

    const links = screen.getAllByRole("link");
    expect(links.map((link) => link.textContent)).toEqual(["Contests", "Audit"]);
    expect(links[0]).toHaveAttribute("aria-current", "page");
    for (const link of links) expect(link.className.split(/\s+/)).toContain("min-h-12");
  });
});
