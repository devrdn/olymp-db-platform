import { render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { ConsolePreview, PREVIEW_COLUMNS, PREVIEW_QUERY, PREVIEW_ROWS } from "./console-preview";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

describe("the look at the console", () => {
  /**
   * Built from the console's parts, so nothing in it may still be usable: the
   * product is a sign-in away.
   */
  test("shows the product without offering to be used", () => {
    render(<ConsolePreview dict={en} />);

    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(screen.getByText(/SELECT/)).toBeInTheDocument();
  });

  test("holds nothing a keyboard can reach", () => {
    const { container } = render(<ConsolePreview dict={en} />);

    const reachable = container.querySelectorAll(
      "a, button, input, textarea, select, [tabindex], [contenteditable]",
    );
    expect(reachable).toHaveLength(0);
  });

  /** Keywords must arrive highlighted, not as one grey block. */
  test("colours the statement with the product's own highlighting", () => {
    const { container } = render(<ConsolePreview dict={en} />);

    const keywords = [...container.querySelectorAll('[data-token="keyword"]')].map(
      (span) => span.textContent,
    );
    expect(keywords).toContain("SELECT");
    expect(keywords).toContain("FROM");
  });

  /** The answer appears under the query. */
  test("puts the answer under the query, row for row", () => {
    render(<ConsolePreview dict={en} />);

    // One header row plus the answer's rows.
    expect(screen.getAllByRole("row")).toHaveLength(PREVIEW_ROWS.length + 1);
    for (const [name] of PREVIEW_ROWS) {
      expect(screen.getByText(name)).toBeInTheDocument();
    }
  });

  /** The table's headers must match the columns the statement selects. */
  test("names the columns the query asked for", () => {
    render(<ConsolePreview dict={en} />);

    const headers = screen.getAllByRole("columnheader").map((cell) => cell.textContent);
    expect(headers).toHaveLength(PREVIEW_COLUMNS.length);
    for (const column of PREVIEW_COLUMNS) {
      expect(PREVIEW_QUERY).toContain(column);
      expect(headers.some((header) => header?.startsWith(column))).toBe(true);
    }
  });

  test("says in words what the frame is", () => {
    render(<ConsolePreview dict={en} />);

    expect(screen.getByRole("heading", { level: 2 })).toHaveTextContent(en.home.console.heading);
    expect(screen.getByText(en.home.console.lede)).toBeInTheDocument();
    expect(screen.getByText(en.home.console.tab)).toBeInTheDocument();
  });

  /** Hidden below the breakpoint, where it would show nothing useful. */
  test("is not shown at all on a narrow screen", () => {
    const { container } = render(<ConsolePreview dict={en} />);

    expect(container.firstElementChild).toHaveClass("max-narrow:hidden");
    expect(container.querySelector("section")).not.toBeNull();
  });
});
