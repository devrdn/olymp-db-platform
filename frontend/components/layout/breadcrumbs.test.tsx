import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import { Breadcrumbs } from "./breadcrumbs";

const trail = [
  { href: "/contests", label: "Contests" },
  { href: "/contests/c-1", label: "The Library Murder" },
  { label: "Story" },
];

describe("Breadcrumbs", () => {
  test("links every step except the one the visitor is standing on", () => {
    render(<Breadcrumbs items={trail} label="Breadcrumb" />);

    expect(screen.getByRole("link", { name: "Contests" })).toHaveAttribute("href", "/contests");
    expect(screen.getByRole("link", { name: "The Library Murder" })).toHaveAttribute(
      "href",
      "/contests/c-1",
    );
    expect(screen.queryByRole("link", { name: "Story" })).not.toBeInTheDocument();
  });

  test("marks where the visitor is, for a reader who cannot see the trail", () => {
    render(<Breadcrumbs items={trail} label="Breadcrumb" />);

    expect(screen.getByText("Story")).toHaveAttribute("aria-current", "page");
  });

  test("names the trail, since a page can carry more than one navigation", () => {
    // Two unnamed <nav>s leave a screen-reader user choosing between
    // "navigation" and "navigation".
    render(<Breadcrumbs items={trail} label="Breadcrumb" />);

    expect(screen.getByRole("navigation", { name: "Breadcrumb" })).toBeInTheDocument();
  });

  test("keeps the separators out of what is read aloud", () => {
    const { container } = render(<Breadcrumbs items={trail} label="Breadcrumb" />);

    const separators = container.querySelectorAll("li [aria-hidden='true']");
    expect(separators).toHaveLength(2);
    expect(screen.getByRole("navigation")).toHaveAccessibleName("Breadcrumb");
  });

  test("renders a single step without a leading separator", () => {
    const { container } = render(
      <Breadcrumbs items={[{ label: "Contests" }]} label="Breadcrumb" />,
    );

    expect(container.querySelectorAll("li [aria-hidden='true']")).toHaveLength(0);
  });
});
