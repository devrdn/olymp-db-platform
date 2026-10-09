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
});
