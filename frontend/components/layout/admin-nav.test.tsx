import { render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";

vi.mock("next/navigation", () => ({ usePathname: () => "/contests/c-1/story" }));

import { AdminNav } from "./admin-nav";

const items = [
  { href: "/contests", label: "Contests" },
  { href: "/audit", label: "Audit" },
];

describe("AdminNav", () => {
  test("keeps the section marked while the visitor is deeper inside it", () => {
    render(<AdminNav items={items} />);

    expect(screen.getByRole("link", { name: "Contests" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Audit" })).not.toHaveAttribute("aria-current");
  });

  test("carries no glyph beside the words", () => {
    // SPEC section 2, rule 1: the interface around the data is rules and
    // typography. Two destinations are told apart by reading them, so an icon
    // here is a mark to look past rather than a shape to aim at.
    const { container } = render(<AdminNav items={items} />);

    expect(container.querySelectorAll("svg")).toHaveLength(0);
    expect(screen.getByRole("link", { name: "Contests" })).toHaveAccessibleName("Contests");
  });
});
