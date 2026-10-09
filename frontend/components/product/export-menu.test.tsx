import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import { ExportMenu } from "./export-menu";

describe("ExportMenu", () => {
  test("hands the download to the browser as a link, not to a script", () => {
    // The server decides what leaves, under the endpoint's permission; a
    // page-built blob could only hold what the page had (nothing, for the
    // contest package).
    render(
      <ExportMenu
        heading="Export"
        formats={[
          { format: "JSON", href: "/api/v1/contests/c1/export", label: "Download the contest package" },
        ]}
      />,
    );

    const link = screen.getByRole("link", { name: "Download the contest package" });
    expect(link).toHaveAttribute("href", "/api/v1/contests/c1/export");
    expect(link).toHaveAttribute("download");
    expect(link).toHaveTextContent("JSON");
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  test("offers one control per format", () => {
    // A list even for one format, since more are planned (docs/ARCHITECTURE.md
    // §9.1).
    render(
      <ExportMenu
        heading="Export"
        formats={[
          { format: "CSV", href: "/a.csv", label: "Download as CSV" },
          { format: "JSON", href: "/a.json", label: "Download as JSON" },
        ]}
      />,
    );

    expect(screen.getAllByRole("link")).toHaveLength(2);
    expect(screen.getByRole("navigation", { name: "Export" })).toBeInTheDocument();
  });

  test("renders nothing at all when a screen has no export to offer", () => {
    // A heading over no controls is still announced.
    const { container } = render(<ExportMenu heading="Export" formats={[]} />);

    expect(container).toBeEmptyDOMElement();
  });
});
