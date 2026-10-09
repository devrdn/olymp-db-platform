import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

import { ResultPanel } from "./result-panel";
import type { ConsoleState } from "./actions";

function show(state: ConsoleState, contestId = "c1") {
  return render(<ResultPanel contestId={contestId} state={state} dict={en} />);
}

afterEach(() => {
  window.localStorage.clear();
});

describe("the result panel", () => {
  test("says nothing has run yet before the first query", () => {
    show({ kind: "idle" });

    expect(screen.getByText(en.participant.play.workspace.resultEmpty)).toBeInTheDocument();
  });

  test("shows the rows it was given", () => {
    show({
      kind: "answer",
      result: { columns: ["id", "note"], rows: [["1", "a knife"]], truncated: false, rows_affected: 0 },
    });

    const table = screen.getByRole("table");
    expect(within(table).getByText("a knife")).toBeInTheDocument();
    expect(within(table).getByRole("columnheader", { name: "note" })).toBeInTheDocument();
  });

  // NULL and the empty string are different things in SQL, and a participant
  // debugging a left join is looking at exactly that column.
  test("shows a null as a null rather than as an empty cell", () => {
    show({ kind: "answer", result: { columns: ["alibi"], rows: [[null]], truncated: false, rows_affected: 0 } });

    expect(within(screen.getByRole("table")).getByText(en.participant.console.null)).toBeInTheDocument();
  });

  test("says when the answer is longer than what is shown", () => {
    show({ kind: "answer", result: { columns: ["g"], rows: [["1"]], truncated: true, rows_affected: 0 } });

    expect(screen.getByText(/longer than that/i)).toBeInTheDocument();
  });

  test("a write answers with a count rather than a table", () => {
    show({ kind: "answer", result: { columns: [], rows: [], truncated: false, rows_affected: 3 } });

    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    expect(screen.getByText(/3 rows changed/i)).toBeInTheDocument();
  });

  test("a refusal names what it was about", () => {
    show({ kind: "refused", code: "query_function_not_supported", subject: "pg_sleep" });

    const status = screen.getByRole("status");
    expect(status).toHaveTextContent(en.errors.query_function_not_supported);
    expect(status).toHaveTextContent("pg_sleep");
  });

  test("an unrecognised code still says something", () => {
    show({ kind: "refused", code: "something_from_the_future" });

    expect(screen.getByRole("status")).toHaveTextContent(en.errors.fallback);
  });

  test("carries a reference for a fault, and not for an ordinary refusal", () => {
    const { unmount } = show({ kind: "refused", code: "query_service_down", requestId: "req-42" });
    expect(screen.getByRole("status")).toHaveTextContent("req-42");
    unmount();

    show({ kind: "refused", code: "query_too_often", requestId: "req-42" });
    expect(screen.getByRole("status")).not.toHaveTextContent("req-42");
  });

  test("a query the database refused shows the database's own words, as a refusal rather than a fault", () => {
    const reason = 'ERROR: column "alibi" does not exist (SQLSTATE 42703)';
    show({ kind: "refused", code: "query_database_error", subject: reason, requestId: "req-42" });

    const status = screen.getByRole("status");
    expect(status).toHaveTextContent(en.errors.query_database_error);
    expect(status).toHaveTextContent(reason);
    expect(status).not.toHaveTextContent(en.errors.invalid_request);
    expect(status).not.toHaveTextContent("req-42");
  });

  test("a query the checks refused carries no reference, because nothing on our side went wrong", () => {
    for (const code of ["query_parse_error", "query_function_not_supported", "query_declined", "query_timed_out"]) {
      const { unmount } = show({ kind: "refused", code, subject: "x", requestId: "req-42" });
      expect(screen.getByRole("status"), code).not.toHaveTextContent("req-42");
      unmount();
    }
  });

  test("a fault, or a code this build cannot name, keeps its reference", () => {
    for (const code of ["internal_error", "game_cluster_full", "something_from_the_future"]) {
      const { unmount } = show({ kind: "refused", code, requestId: "req-42" });
      expect(screen.getByRole("status"), code).toHaveTextContent("req-42");
      unmount();
    }
  });

  test("offers a download only when there are rows to download", () => {
    const { unmount } = show({ kind: "idle" });
    expect(screen.queryByRole("button", { name: en.participant.play.workspace.download })).not.toBeInTheDocument();
    unmount();

    show({ kind: "answer", result: { columns: ["id"], rows: [["1"]], truncated: false, rows_affected: 0 } });
    expect(screen.getByRole("button", { name: en.participant.play.workspace.download })).toBeInTheDocument();
  });

  // The download carries exactly the rows in state, never more than the
  // screen showed; checked against the generated file.
  test("downloads exactly the rows already shown, as CSV", async () => {
    const created = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:mock");
    const revoked = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
    const clicked = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});

    show({
      kind: "answer",
      result: { columns: ["id", "note"], rows: [["1", "a knife"]], truncated: false, rows_affected: 0 },
    });
    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.download }));

    expect(created).toHaveBeenCalledTimes(1);
    const blob = created.mock.calls[0][0] as Blob;
    expect(blob.type).toBe("text/csv;charset=utf-8");
    expect(await blob.text()).toBe("id,note\r\n1,a knife\r\n");
    expect(clicked).toHaveBeenCalledTimes(1);
    expect(revoked).toHaveBeenCalledWith("blob:mock");
  });
});

/** The meter row and the column types around the table. */
describe("what the table says about itself", () => {
  const t = en.participant.console;

  test("counts the rows and says how long the statement took", () => {
    render(
      <ResultPanel
        contestId="c1"
        state={{
          kind: "answer",
          result: {
            columns: ["id"],
            column_types: ["uuid"],
            rows: [["1"], ["2"]],
            truncated: false,
            rows_affected: 0,
            duration_micros: 38_000,
          },
        }}
        dict={en}
      />,
    );

    // The count beside its label: a two-row result also has a cell reading "2".
    const meter = screen.getByText(t.meter.rows).closest("span");
    expect(meter).toHaveTextContent("2");
    expect(screen.getByText(t.meter.ms.replace("{n}", "38"))).toBeInTheDocument();
  });

  // An unmeasured duration is not a measured zero.
  test("says nothing about time when the answer carried no duration", () => {
    render(
      <ResultPanel
        contestId="c1"
        state={{
          kind: "answer",
          result: { columns: ["id"], rows: [["1"]], truncated: false, rows_affected: 0 },
        }}
        dict={en}
      />,
    );

    expect(screen.queryByText(t.meter.time)).not.toBeInTheDocument();
  });

  test("prints each column's type under its name", () => {
    render(
      <ResultPanel
        contestId="c1"
        state={{
          kind: "answer",
          result: {
            columns: ["full_name", "at"],
            column_types: ["text", "timestamp with time zone"],
            rows: [["Margot", "2024-11-09"]],
            truncated: false,
            rows_affected: 0,
          },
        }}
        dict={en}
      />,
    );

    expect(screen.getByText("text")).toBeInTheDocument();
    expect(screen.getByText("timestamp with time zone")).toBeInTheDocument();
  });

  // An organiser's enum exists only in its game database, and the driver's
  // type map cannot name it: no label rather than a wrong one.
  test("leaves a column bare when its type could not be named", () => {
    render(
      <ResultPanel
        contestId="c1"
        state={{
          kind: "answer",
          result: {
            columns: ["mood"],
            column_types: [""],
            rows: [["cheerful"]],
            truncated: false,
            rows_affected: 0,
          },
        }}
        dict={en}
      />,
    );

    const header = screen.getByRole("columnheader");
    expect(header).toHaveTextContent("mood");
    expect(header.querySelector("span")).toBeNull();
  });
});

/**
 * The table renders only the rows the pane can show, from declared column
 * widths. jsdom has no layout, so these assert which rows exist, what the
 * table reports about the rest, and that clipping never loses a value; the
 * scroll box's height is stated.
 */
describe("a large result", () => {
  function answer(rows: number, columns = 8): ConsoleState {
    return {
      kind: "answer",
      result: {
        columns: Array.from({ length: columns }, (_, c) => `col_${c}`),
        rows: Array.from({ length: rows }, (_, r) =>
          Array.from({ length: columns }, (_, c) => `cell-${r}-${c}`),
        ),
        truncated: false,
        rows_affected: 0,
      },
    };
  }

  /** The scroll box, with a stated height jsdom cannot compute. */
  function scrollerOf(table: HTMLElement, height: number): HTMLElement {
    const scroller = table.parentElement as HTMLElement;
    Object.defineProperty(scroller, "clientHeight", { value: height, configurable: true });
    Object.defineProperty(scroller, "scrollTop", { value: 0, configurable: true, writable: true });
    return scroller;
  }

  test("puts a window of rows in the DOM rather than the whole answer", () => {
    show(answer(1000));

    const table = screen.getByRole("table");
    // The header plus what fits, not a thousand and one.
    expect(within(table).getAllByRole("row").length).toBeLessThan(80);
  });

  // A partly rendered table still reports its full size to screen readers.
  test("still says how many rows the whole answer has", () => {
    show(answer(1000));

    expect(screen.getByRole("table")).toHaveAttribute("aria-rowcount", "1001");
  });

  test("scrolling exchanges the window for the rows now under it", () => {
    show(answer(1000));
    const table = screen.getByRole("table");
    const scroller = scrollerOf(table, 440);

    // Ten rows of 44px per hundred pixels: row 500 is 22000px down.
    (scroller as unknown as { scrollTop: number }).scrollTop = 500 * 44;
    fireEvent.scroll(scroller);

    expect(within(table).queryByText("cell-0-0")).not.toBeInTheDocument();
    expect(within(table).getByText("cell-500-0")).toBeInTheDocument();
    // And each row still knows which row of the answer it is.
    expect(within(table).getByText("cell-500-0").closest("tr")).toHaveAttribute(
      "aria-rowindex",
      String(500 + 2),
    );
  });

  test("a value clipped by its column keeps its whole text on the cell", () => {
    const statement = "the witness said ".repeat(30);
    show({
      kind: "answer",
      result: { columns: ["note"], rows: [[statement]], truncated: false, rows_affected: 0 },
    });

    expect(screen.getByRole("cell")).toHaveAttribute("title", statement);
  });

  // A declared width per column lets the browser place the first row
  // without measuring the rest.
  test("declares a width for every column", () => {
    show(answer(20, 4));

    const table = screen.getByRole("table");
    expect(table.querySelectorAll("colgroup > col")).toHaveLength(4);
    expect(table.className).toContain("table-fixed");
  });
});

/** A result names the tab it came from, since it outlives a tab switch. */
describe("which tab the result came from", () => {
  test("names the tab above the answer", () => {
    render(
      <ResultPanel
        contestId="c1"
        state={{ kind: "answer", result: { columns: ["id"], rows: [["1"]], truncated: false, rows_affected: 0 } }}
        sourceTitle="Suspects"
        dict={en}
      />,
    );

    expect(
      screen.getByText(en.participant.play.workspace.resultFrom.replace("{tab}", "Suspects")),
    ).toBeInTheDocument();
  });

  test("names it above a refusal too", () => {
    render(
      <ResultPanel
        contestId="c1"
        state={{ kind: "refused", code: "query_syntax_error" }}
        sourceTitle="Suspects"
        dict={en}
      />,
    );

    expect(
      screen.getByText(en.participant.play.workspace.resultFrom.replace("{tab}", "Suspects")),
    ).toBeInTheDocument();
  });

  test("says nothing about a tab before anything has been run", () => {
    render(<ResultPanel contestId="c1" state={{ kind: "idle" }} sourceTitle={null} dict={en} />);

    expect(screen.queryByText(/^From /)).not.toBeInTheDocument();
  });

  // The heading is built from what it was handed, and "From " followed by
  // nothing is worse than no heading.
  test("draws no heading for a run whose tab has no name", () => {
    render(
      <ResultPanel
        contestId="c1"
        state={{
          kind: "answer",
          result: { columns: ["id"], rows: [["1"]], truncated: false, rows_affected: 0 },
        }}
        sourceTitle=""
        dict={en}
      />,
    );

    // In Russian and Romanian the template quotes the name, so it would be a
    // pair of empty quotes.
    expect(screen.queryByText("From")).not.toBeInTheDocument();
  });
});

/**
 * SPEC.md §5: a clipped cell opens in full under the table. The table is windowed,
 * so the selection must never be a DOM node, which vanishes on scroll.
 */
describe("opening one row of the result", () => {
  const t = en.participant.play.workspace.row;

  function answer(rows: number, columns = 2): ConsoleState {
    return {
      kind: "answer",
      result: {
        columns: Array.from({ length: columns }, (_, c) => `col_${c}`),
        rows: Array.from({ length: rows }, (_, r) =>
          Array.from({ length: columns }, (_, c) => `cell-${r}-${c}`),
        ),
        truncated: false,
        rows_affected: 0,
      },
    };
  }

  /** The scroll box, with a stated height jsdom cannot compute. */
  function scrollerOf(table: HTMLElement, height = 440): HTMLElement {
    const scroller = table.parentElement as HTMLElement;
    Object.defineProperty(scroller, "clientHeight", { value: height, configurable: true });
    Object.defineProperty(scroller, "scrollTop", { value: 0, configurable: true, writable: true });
    return scroller;
  }

  function rowOf(index: number): HTMLElement {
    return within(screen.getByRole("table"))
      .getByText(`cell-${index}-0`)
      .closest("tr") as HTMLElement;
  }

  test("opens on a click, and marks the row it opened", async () => {
    const user = userEvent.setup();
    show(answer(5));

    await user.click(rowOf(2));

    expect(screen.getByRole("region", { name: t.region.replace("{n}", "3") })).toBeInTheDocument();
    expect(rowOf(2)).toHaveAttribute("aria-selected", "true");
    expect(rowOf(1)).toHaveAttribute("aria-selected", "false");
  });

  test("opens on Enter and on Space, from a row the keyboard can reach", async () => {
    const user = userEvent.setup();
    show(answer(5));

    // One row is in the tab order, so the rest is reachable without a pointer.
    expect(rowOf(0)).toHaveAttribute("tabindex", "0");

    rowOf(1).focus();
    await user.keyboard("{Enter}");
    expect(screen.getByRole("region", { name: t.region.replace("{n}", "2") })).toBeInTheDocument();

    await user.keyboard("{Escape}");
    rowOf(3).focus();
    await user.keyboard(" ");
    expect(screen.getByRole("region", { name: t.region.replace("{n}", "4") })).toBeInTheDocument();
  });

  // Neither `aria-selected` on a table row nor the new panel is announced,
  // so focus moves into the named region; closing returns it to the row.
  test("opening a row takes the keyboard into the panel, and closing gives it back", async () => {
    const user = userEvent.setup();
    show(answer(5));

    await user.click(rowOf(2));

    expect(screen.getByRole("region", { name: t.region.replace("{n}", "3") })).toHaveFocus();

    await user.keyboard("{Escape}");
    expect(rowOf(2)).toHaveFocus();
  });

  test("the arrows walk to the neighbouring row without closing the panel", async () => {
    const user = userEvent.setup();
    show(answer(10));

    await user.click(rowOf(4));
    await user.keyboard("{ArrowDown}");

    expect(screen.getByRole("region", { name: t.region.replace("{n}", "6") })).toBeInTheDocument();
    expect(rowOf(5)).toHaveAttribute("aria-selected", "true");

    await user.keyboard("{ArrowUp}{ArrowUp}");
    expect(screen.getByRole("region", { name: t.region.replace("{n}", "4") })).toBeInTheDocument();

    // They stop at the ends rather than wrapping.
    rowOf(0).focus();
    await user.click(rowOf(0));
    await user.keyboard("{ArrowUp}");
    expect(screen.getByRole("region", { name: t.region.replace("{n}", "1") })).toBeInTheDocument();
  });

  test("closes on Esc and on the button", async () => {
    const user = userEvent.setup();
    show(answer(5));

    await user.click(rowOf(1));
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("region", { name: /Row \d+ of the result/ })).not.toBeInTheDocument();

    await user.click(rowOf(1));
    await user.click(screen.getByRole("button", { name: t.close }));
    expect(screen.queryByRole("region", { name: /Row \d+ of the result/ })).not.toBeInTheDocument();
  });

  // A new run is a new answer; row 4 of the old one means nothing.
  test("a new run closes the panel", async () => {
    const user = userEvent.setup();
    const { rerender } = show(answer(10));

    await user.click(rowOf(3));
    expect(screen.getByRole("region", { name: t.region.replace("{n}", "4") })).toBeInTheDocument();

    rerender(<ResultPanel contestId="c1" state={answer(10)} dict={en} />);
    expect(screen.queryByRole("region", { name: /Row \d+ of the result/ })).not.toBeInTheDocument();
  });

  test("a refusal takes the panel with it", async () => {
    const user = userEvent.setup();
    const { rerender } = show(answer(10));

    await user.click(rowOf(3));
    rerender(<ResultPanel contestId="c1" state={{ kind: "refused", code: "query_syntax_error" }} dict={en} />);

    expect(screen.queryByRole("region", { name: /Row \d+ of the result/ })).not.toBeInTheDocument();
  });

  // Scrolling takes the selected row out of the DOM; the panel still shows
  // it, and scrolling back finds it still marked.
  test("survives the row scrolling out of the window", async () => {
    const user = userEvent.setup();
    show(answer(1000));
    const table = screen.getByRole("table");
    const scroller = scrollerOf(table);

    await user.click(rowOf(3));

    (scroller as unknown as { scrollTop: number }).scrollTop = 500 * 44;
    fireEvent.scroll(scroller);

    expect(within(table).queryByText("cell-3-0")).not.toBeInTheDocument();
    expect(screen.getByRole("region", { name: t.region.replace("{n}", "4") })).toBeInTheDocument();
    expect(within(screen.getByRole("region", { name: t.region.replace("{n}", "4") })).getByText("cell-3-1")).toBeInTheDocument();

    (scroller as unknown as { scrollTop: number }).scrollTop = 0;
    fireEvent.scroll(scroller);
    expect(rowOf(3)).toHaveAttribute("aria-selected", "true");
  });

  // Arrowing past the pane's bottom must scroll the row into view.
  test("scrolls the virtualised table to a row the arrows walked to", async () => {
    const user = userEvent.setup();
    show(answer(1000));
    const table = screen.getByRole("table");
    const scroller = scrollerOf(table, 440);

    await user.click(rowOf(0));
    for (let i = 0; i < 20; i++) await user.keyboard("{ArrowDown}");

    expect(screen.getByRole("region", { name: t.region.replace("{n}", "21") })).toBeInTheDocument();
    // Ten rows of 44px per hundred pixels: row 20 ends at 924px, and a 440px
    // box showing it has to have scrolled at least to 484.
    expect(scroller.scrollTop).toBeGreaterThanOrEqual(21 * 44 - 440);
  });

  test("the panel and the table share a draggable edge, remembered per contest", async () => {
    const user = userEvent.setup();
    const first = show(answer(5));

    await user.click(rowOf(2));
    const handle = screen.getByRole("separator", {
      name: en.participant.play.workspace.panes.detail,
    });
    expect(handle).toHaveAttribute("aria-orientation", "horizontal");

    const before = Number(handle.getAttribute("aria-valuenow"));
    handle.focus();
    await user.keyboard("{Shift>}{ArrowUp}{/Shift}");
    expect(handle).toHaveAttribute("aria-valuenow", String(before - 4));
    first.unmount();

    show(answer(5));
    await user.click(rowOf(2));
    expect(
      screen.getByRole("separator", { name: en.participant.play.workspace.panes.detail }),
    ).toHaveAttribute("aria-valuenow", String(before - 4));
  });

  test("offers no edge to drag while no row is open", () => {
    show(answer(5));

    expect(
      screen.queryByRole("separator", { name: en.participant.play.workspace.panes.detail }),
    ).not.toBeInTheDocument();
  });

  // Below `narrow` the bottom panel has only a `max-height`, so a grid share
  // resolves as `auto` and pushes the open row out of view; there the two
  // become a flex column. jsdom has no media queries, so both arrangements
  // are checked as declared.
  test("falls back to a bounded column where the pane has no height of its own", async () => {
    const user = userEvent.setup();
    show(answer(5));

    await user.click(rowOf(2));
    const split = screen.getByRole("table").closest("[style*='--pane-detail']") as HTMLElement;

    expect(split.className).toContain("grid-rows-[minmax(0,var(--pane-detail))_auto_minmax(0,1fr)]");
    expect(split.className).toMatch(/(^|\s)max-narrow:flex(\s|$)/);
    expect(split.className).toMatch(/(^|\s)max-narrow:flex-col(\s|$)/);

    // Each pane takes a share and scrolls inside itself.
    const scroller = screen.getByRole("table").parentElement as HTMLElement;
    expect(scroller.className).toMatch(/(^|\s)flex-1(\s|$)/);
    expect(scroller.className).toMatch(/(^|\s)min-h-0(\s|$)/);

    const panel = screen.getByRole("region", { name: t.region.replace("{n}", "3") });
    expect(panel.className).toMatch(/(^|\s)max-narrow:flex-1(\s|$)/);
    expect(panel.className).toMatch(/(^|\s)min-h-0(\s|$)/);
    expect((panel.querySelector("dl") as HTMLElement).className).toMatch(/(^|\s)overflow-auto(\s|$)/);

    // Nothing to divide there.
    expect(
      screen.getByRole("separator", { name: en.participant.play.workspace.panes.detail }).className,
    ).toMatch(/(^|\s)max-narrow:hidden(\s|$)/);
  });

  // The tab stop follows the keyboard, so tabbing back returns to the same
  // row.
  test("the tab stop follows the arrows even with no row open", async () => {
    const user = userEvent.setup();
    show(answer(20));

    rowOf(0).focus();
    await user.keyboard("{ArrowDown}{ArrowDown}{ArrowDown}");

    // Nothing opened: the arrows are ordinary table navigation here.
    expect(screen.queryByRole("region", { name: /Row \d+ of the result/ })).not.toBeInTheDocument();
    expect(rowOf(3)).toHaveFocus();
    expect(rowOf(3)).toHaveAttribute("tabindex", "0");
    expect(rowOf(0)).toHaveAttribute("tabindex", "-1");
  });
});
