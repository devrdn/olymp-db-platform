import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import { ExportMenu } from "./export-menu";

describe("ExportMenu", () => {
  test("hands the download to the browser as a link, not to a script", () => {
    // The whole design decision, held here: what leaves the server is decided
    // by the server, under the permission the endpoint sits behind, and the
    // page only asks for it. A button that fetched and built a blob could
    // only ever offer what the page already had — which for the contest
    // package is nothing, because the answer key is never sent to a page.
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
    // §9.1 promises NDJSON and XLSX beside CSV. Each surface offers one
    // format today, so the shape has to be a list rather than a special case
    // for one.
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
    // A group heading with no controls under it is a promise the screen does
    // not keep, and a screen reader announces it all the same.
    const { container } = render(<ExportMenu heading="Export" formats={[]} />);

    expect(container).toBeEmptyDOMElement();
  });
});
