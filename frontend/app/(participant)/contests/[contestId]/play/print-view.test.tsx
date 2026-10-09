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

  // An identity that could not be read: the byline gives only the date
  // rather than a name-shaped hole.
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

    // No control of its own: printing starts from the story tab.
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    // The app bar belongs to the layout above the route, so nothing here can
    // render navigation.
    expect(screen.queryByRole("navigation")).not.toBeInTheDocument();
  });
});
