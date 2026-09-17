import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

import { ResultPanel } from "./result-panel";
import type { ConsoleState } from "./actions";

function show(state: ConsoleState) {
  return render(<ResultPanel state={state} dict={en} />);
}

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

  // Task 3's own boundary: the download must never become a way to take more
  // than the screen already showed. Built from exactly the rows in state —
  // proven here by checking what the generated file actually contains.
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

/**
 * The meter row and the column types the design puts around the table
 * (docs/design/preview.html, "SQL-консоль"). Both arrived on the wire only
 * recently; before that the table said how many rows in a sentence and named
 * no types at all.
 */
describe("what the table says about itself", () => {
  const t = en.participant.console;

  test("counts the rows and says how long the statement took", () => {
    render(
      <ResultPanel
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

    // The count lives beside its own label, not anywhere the digit happens
    // to appear — a two-row result also has a cell reading "2".
    const meter = screen.getByText(t.meter.rows).closest("span");
    expect(meter).toHaveTextContent("2");
    expect(screen.getByText(t.meter.ms.replace("{n}", "38"))).toBeInTheDocument();
  });

  // An unmeasured duration and a measured zero are different facts, and
  // printing "0 ms" for the first reads as a broken meter.
  test("says nothing about time when the answer carried no duration", () => {
    render(
      <ResultPanel
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

  // Better a missing label than a wrong one: an organiser's own enum exists
  // only in that one game database, and the driver's type map cannot name it.
  test("leaves a column bare when its type could not be named", () => {
    render(
      <ResultPanel
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
 * Finding 1: the table only ever puts the rows the pane can show into the
 * DOM, and lays itself out from declared column widths rather than from a
 * measurement of every cell.
 *
 * jsdom has no layout, so nothing here asserts a size — what is asserted is
 * which rows exist, what the table says about the ones that do not, and that
 * clipping a value never loses it. The scroll box's height is stated rather
 * than measured for the same reason.
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

  /** The scroll box around the table, with a stated height jsdom cannot work out for itself. */
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

  // A table holding only some of its rows still has to say how many there
  // are, or a screen reader is told the answer is forty rows long.
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

  // The half of finding 1 that costs nothing: a declared width per column is
  // what lets the browser place the first row without measuring the rest.
  test("declares a width for every column", () => {
    show(answer(20, 4));

    const table = screen.getByRole("table");
    expect(table.querySelectorAll("colgroup > col")).toHaveLength(4);
    expect(table.className).toContain("table-fixed");
  });
});

/**
 * A result outlives the tab it came from: the participant runs a query, then
 * goes on typing in another tab while reading the answer. The heading says
 * which tab that answer belongs to (§5 of the workspace design).
 */
describe("which tab the result came from", () => {
  test("names the tab above the answer", () => {
    render(
      <ResultPanel
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
    render(<ResultPanel state={{ kind: "refused", code: "query_syntax_error" }} sourceTitle="Suspects" dict={en} />);

    expect(
      screen.getByText(en.participant.play.workspace.resultFrom.replace("{tab}", "Suspects")),
    ).toBeInTheDocument();
  });

  test("says nothing about a tab before anything has been run", () => {
    render(<ResultPanel state={{ kind: "idle" }} sourceTitle={null} dict={en} />);

    expect(screen.queryByText(/^From /)).not.toBeInTheDocument();
  });
});
