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
   * The whole reason this is a replica rather than a screenshot is that it is
   * built from the console's own parts — and the whole risk of that choice is
   * that one of those parts still works. A visitor who presses a run button
   * that does nothing, or types into a textarea nobody reads, has been told
   * the product is here when it is a sign-in away.
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

  /**
   * The statement is the one thing here a reader is asked to read rather than
   * skim, and it is coloured by the product's own highlighter — so the
   * keywords have to have arrived as keywords rather than as one grey block.
   */
  test("colours the statement with the product's own highlighting", () => {
    const { container } = render(<ConsolePreview dict={en} />);

    const keywords = [...container.querySelectorAll('[data-token="keyword"]')].map(
      (span) => span.textContent,
    );
    expect(keywords).toContain("SELECT");
    expect(keywords).toContain("FROM");
  });

  /** A demonstration with no answer under it demonstrates half the product. */
  test("puts the answer under the query, row for row", () => {
    render(<ConsolePreview dict={en} />);

    // One header row, and one for each row of the answer.
    expect(screen.getAllByRole("row")).toHaveLength(PREVIEW_ROWS.length + 1);
    for (const [name] of PREVIEW_ROWS) {
      expect(screen.getByText(name)).toBeInTheDocument();
    }
  });

  /**
   * The columns a result table names are the ones the database returned, so
   * the two halves of the frame have to agree: a header the statement never
   * asked for is the kind of lie a screenshot tells after the product moves.
   */
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

  /**
   * A console shrunk to 375px is a column of clipped identifiers over a table
   * one column wide — a demonstration of nothing at all. Below the layout's
   * one breakpoint the section is not shown, which is the only honest size
   * for it.
   */
  test("is not shown at all on a narrow screen", () => {
    const { container } = render(<ConsolePreview dict={en} />);

    expect(container.firstElementChild).toHaveClass("max-narrow:hidden");
    expect(container.querySelector("section")).not.toBeNull();
  });
});
