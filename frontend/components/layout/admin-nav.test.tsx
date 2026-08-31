import { render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";

vi.mock("next/navigation", () => ({ usePathname: () => "/contests/c-1/story" }));

import { AdminNav } from "./admin-nav";

const items = [
  { href: "/contests", label: "Contests", icon: "contests" as const },
  { href: "/audit", label: "Audit", icon: "audit" as const },
];

describe("AdminNav", () => {
  test("keeps the section marked while the visitor is deeper inside it", () => {
    render(<AdminNav items={items} />);

    expect(screen.getByRole("link", { name: "Contests" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Audit" })).not.toHaveAttribute("aria-current");
  });

  test("draws an icon that the accessible name does not repeat", () => {
    // The icon is decoration beside a word, not a second label: announced, it
    // would make every destination read twice to a screen reader.
    const { container } = render(<AdminNav items={items} />);

    const icons = container.querySelectorAll("svg[aria-hidden='true']");
    expect(icons).toHaveLength(2);
    expect(screen.getByRole("link", { name: "Contests" })).toHaveAccessibleName("Contests");
  });
});
