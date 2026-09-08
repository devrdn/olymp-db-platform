import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

import { PrintView } from "./print-view";

describe("PrintView", () => {
  test("names the contest, the reader, and when it was printed", () => {
    render(
      <PrintView
        contestTitle="The Warehouse Fire"
        participantName="Ada Lovelace"
        date="8 Sep 2026"
        storyMarkdown="A body was found in the stacks."
        dict={en}
      />,
    );

    expect(screen.getByRole("heading", { level: 1, name: "The Warehouse Fire" })).toBeInTheDocument();
    expect(screen.getByText("Printed by Ada Lovelace on 8 Sep 2026")).toBeInTheDocument();
    expect(screen.getByText("A body was found in the stacks.")).toBeInTheDocument();
  });

  // The rare edge print/page.tsx's own doc names: an identity this server
  // could not read. The byline says less rather than reading "Printed by  on
  // 8 Sep 2026" with a name-shaped hole in it.
  test("says only the date when there is no participant name to print", () => {
    render(
      <PrintView
        contestTitle="The Warehouse Fire"
        participantName=""
        date="8 Sep 2026"
        storyMarkdown="A body was found in the stacks."
        dict={en}
      />,
    );

    expect(screen.getByText("Printed on 8 Sep 2026")).toBeInTheDocument();
    expect(screen.queryByText(/Printed by/)).not.toBeInTheDocument();
  });

  test("carries the one control the page needs, and nothing a console screen would", () => {
    render(
      <PrintView
        contestTitle="The Warehouse Fire"
        participantName="Ada Lovelace"
        date="8 Sep 2026"
        storyMarkdown="A body was found in the stacks."
        dict={en}
      />,
    );

    // No control of its own: printing is started from the story tab, and a
    // button hidden on screen and `print:hidden` in print is one nobody can
    // ever press.
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    // No console furniture exists to query for here — this component never
    // imports ConsoleEditor, SchemaPanel, ResultPanel or QueryLogPanel — but
    // the one piece of chrome every screen behind a session wears (AppBar) is
    // outside this component entirely (it belongs to the layout above the
    // route, not to this presentational piece), so there is nothing here that
    // could render it even by accident.
    expect(screen.queryByRole("navigation")).not.toBeInTheDocument();
  });
});
