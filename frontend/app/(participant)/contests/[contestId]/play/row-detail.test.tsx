import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

import { RowDetail } from "./row-detail";

const t = en.participant.play.workspace.row;

function show(
  props: Partial<React.ComponentProps<typeof RowDetail>> = {},
) {
  const onClose = vi.fn();
  const view = render(
    <RowDetail
      columns={["id", "alibi"]}
      columnTypes={["uuid", "text"]}
      row={["7", "at the lighthouse"]}
      index={0}
      nullLabel={en.participant.console.null}
      dict={en}
      onClose={onClose}
      {...props}
    />,
  );
  return { ...view, onClose };
}

/**
 * A clipboard the browser is willing to write to.
 *
 * Stubbed *after* `userEvent.setup()`, which installs a clipboard of its own:
 * what is under test is which of the two ways this component takes, so the
 * one it finds has to be the one this test put there.
 */
function clipboardThatWorks() {
  const writeText = vi.fn(async () => {});
  vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
  return writeText;
}

/** The older way, which the component falls through to. jsdom has no copy of its own. */
function stubExecCommand(exec: (command: string) => boolean) {
  Object.defineProperty(document, "execCommand", { value: exec, configurable: true, writable: true });
}

afterEach(() => {
  vi.unstubAllGlobals();
  Reflect.deleteProperty(document, "execCommand");
});

/** Repeated text without the trailing space a normalised text match would trip over. */
const A_STATEMENT = "the witness said ".repeat(30).trim();

/**
 * §7: the table clips a cell at its column's width, so a long value cannot be
 * read there at all. This panel is where the whole row lives — every column,
 * every value in full, and a way to take one out of the page.
 */
describe("the open row", () => {
  test("names every column of the row and shows each value whole", () => {
    const statement = A_STATEMENT;
    show({ columns: ["id", "note"], columnTypes: undefined, row: ["7", statement] });

    const region = screen.getByRole("region", { name: t.region.replace("{n}", "1") });
    expect(within(region).getByText("id")).toBeInTheDocument();
    expect(within(region).getByText("note")).toBeInTheDocument();
    // Whole, not clipped and not carried on a `title` the way the table has to.
    expect(within(region).getByText(statement)).toBeInTheDocument();
  });

  // The table draws a null as the word, in its own colour; a panel that drew
  // it as an empty line would collapse exactly the distinction a participant
  // debugging a left join opened the row to see.
  test("shows a null the same way the table does", () => {
    show({ columns: ["alibi"], columnTypes: undefined, row: [null] });

    const region = screen.getByRole("region", { name: t.region.replace("{n}", "1") });
    expect(within(region).getByText(en.participant.console.null)).toBeInTheDocument();
  });

  test("wraps a long value rather than clipping it", () => {
    const statement = A_STATEMENT;
    show({ columns: ["note"], columnTypes: undefined, row: [statement] });

    const value = screen.getByText(statement);
    expect(value.className).toMatch(/break-words/);
    expect(value.className).toMatch(/whitespace-pre-wrap/);
    expect(value.className).not.toMatch(/truncate/);
  });

  test("says which row of the answer it is", () => {
    show({ index: 41 });

    expect(screen.getByText(t.heading.replace("{n}", "42"))).toBeInTheDocument();
  });

  // The panel is rendered only while a row is open, so opening one is this
  // component mounting. Nothing else on the screen says it happened: a `tr`
  // marked `aria-selected` in an ordinary table is not announced, and
  // neither is a panel that simply appears. The focus landing on the region
  // reads its name, which is the row number.
  test("takes the keyboard when it opens, without joining the tab order", () => {
    show({ index: 2 });

    const region = screen.getByRole("region", { name: t.region.replace("{n}", "3") });
    expect(region).toHaveFocus();
    expect(region).toHaveAttribute("tabindex", "-1");
  });

  test("closes on the button", async () => {
    const user = userEvent.setup();
    const { onClose } = show();

    await user.click(screen.getByRole("button", { name: t.close }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});

describe("taking a value out of the page", () => {
  test("copies one column's value, named so the button says which", async () => {
    const user = userEvent.setup();
    const writeText = clipboardThatWorks();
    show();

    await user.click(screen.getByRole("button", { name: t.copyValueNamed.replace("{column}", "alibi") }));

    expect(writeText).toHaveBeenCalledWith("at the lighthouse");
    expect(screen.getByRole("status")).toHaveTextContent(t.copied);
  });

  // A null is not the empty string, and neither is what the word NULL would
  // paste as. The cell has no text to take, so the button is not offered.
  test("offers no copy for a column that holds a null", () => {
    show({ columns: ["alibi"], columnTypes: undefined, row: [null] });

    expect(
      screen.queryByRole("button", { name: t.copyValueNamed.replace("{column}", "alibi") }),
    ).not.toBeInTheDocument();
  });

  test("copies the whole row as CSV, header and all", async () => {
    const user = userEvent.setup();
    const writeText = clipboardThatWorks();
    show();

    await user.click(screen.getByRole("button", { name: t.copyRow }));

    expect(writeText).toHaveBeenCalledWith("id,alibi\r\n7,at the lighthouse\r\n");
  });

  // A live region only announces what changes inside it: one created together
  // with its own text is a region the reader never had, and a refused copy —
  // the message that matters most — goes unsaid.
  test("keeps the line that reports a copy on the page before there is one to report", () => {
    show();

    const status = screen.getByRole("status");
    expect(status).toBeEmptyDOMElement();
    // And costs nothing while it is empty.
    expect(status.className).not.toMatch(/(^|\s)py-1(\s|$)/);
  });

  // Not every browser this runs in has the async clipboard on an insecure
  // origin, and a classroom's own machine is exactly where that bites.
  test("falls back to a selection when there is no clipboard API", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("navigator", { ...navigator, clipboard: undefined });
    const exec = vi.fn(() => true);
    stubExecCommand(exec);
    show();

    await user.click(screen.getByRole("button", { name: t.copyValueNamed.replace("{column}", "alibi") }));

    expect(exec).toHaveBeenCalledWith("copy");
    expect(screen.getByRole("status")).toHaveTextContent(t.copied);
    // Nothing of the fallback is left behind in the document.
    expect(document.querySelectorAll("textarea")).toHaveLength(0);
  });

  // Quietly: a refused clipboard is a thing to say in a line under the
  // buttons, not an exception that takes the result panel down with it.
  test("says so, and throws nothing, when the copy is refused", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("navigator", {
      ...navigator,
      clipboard: {
        writeText: async () => {
          throw new Error("denied");
        },
      },
    });
    stubExecCommand(() => {
      throw new Error("denied too");
    });
    show();

    await user.click(screen.getByRole("button", { name: t.copyRow }));

    expect(screen.getByRole("status")).toHaveTextContent(t.copyFailed);
  });

  // The fallback selects a textarea to copy from, and a selection takes the
  // focus with it. Left there, the row's own Esc and arrows stop working —
  // on exactly the plain-HTTP machines the fallback exists for.
  test("gives the focus back to whatever had it before the selection", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("navigator", { ...navigator, clipboard: undefined });
    stubExecCommand(() => true);
    show();

    const button = screen.getByRole("button", { name: t.copyRow });
    await user.click(button);

    expect(button).toHaveFocus();
  });
});
