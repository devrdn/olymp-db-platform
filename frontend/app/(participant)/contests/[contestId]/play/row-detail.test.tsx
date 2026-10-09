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
 * A working clipboard, stubbed after `userEvent.setup()` (which installs its
 * own) so the component finds this one.
 */
function clipboardThatWorks() {
  const writeText = vi.fn(async () => {});
  vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
  return writeText;
}

/** The `execCommand` fallback; jsdom has no copy of its own. */
function stubExecCommand(exec: (command: string) => boolean) {
  Object.defineProperty(document, "execCommand", { value: exec, configurable: true, writable: true });
}

afterEach(() => {
  vi.unstubAllGlobals();
  Reflect.deleteProperty(document, "execCommand");
});

/** Repeated text without a trailing space, which text matching would trip over. */
const A_STATEMENT = "the witness said ".repeat(30).trim();

/**
 * SPEC.md §5: the table clips cells, so the open row shows every value in
 * full and can copy it.
 */
describe("the open row", () => {
  test("names every column of the row and shows each value whole", () => {
    const statement = A_STATEMENT;
    show({ columns: ["id", "note"], columnTypes: undefined, row: ["7", statement] });

    const region = screen.getByRole("region", { name: t.region.replace("{n}", "1") });
    expect(within(region).getByText("id")).toBeInTheDocument();
    expect(within(region).getByText("note")).toBeInTheDocument();
    // Whole, not clipped and not only in a `title`.
    expect(within(region).getByText(statement)).toBeInTheDocument();
  });

  // A null drawn as an empty line would hide the distinction a left-join
  // debugger opened the row to see.
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

  // The panel mounts when a row opens. Neither `aria-selected` nor a new
  // panel is announced, so focus lands on the region and reads its name.
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

  // The word NULL is not the value, so nothing is offered to copy.
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

  // A live region announces only changes inside it, so it must exist before
  // the first message.
  test("keeps the line that reports a copy on the page before there is one to report", () => {
    show();

    const status = screen.getByRole("status");
    expect(status).toBeEmptyDOMElement();
    // And takes no space while empty.
    expect(status.className).not.toMatch(/(^|\s)py-1(\s|$)/);
  });

  // The async clipboard is missing on an insecure origin, as on a
  // classroom's local server.
  test("falls back to a selection when there is no clipboard API", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("navigator", { ...navigator, clipboard: undefined });
    const exec = vi.fn(() => true);
    stubExecCommand(exec);
    show();

    await user.click(screen.getByRole("button", { name: t.copyValueNamed.replace("{column}", "alibi") }));

    expect(exec).toHaveBeenCalledWith("copy");
    expect(screen.getByRole("status")).toHaveTextContent(t.copied);
    // The fallback leaves nothing behind.
    expect(document.querySelectorAll("textarea")).toHaveLength(0);
  });

  // A refused copy is a line of text, not an exception.
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

  // Selecting the fallback textarea takes the focus, which the row's Esc and
  // arrows need back.
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
