import { render, screen, within } from "@testing-library/react";
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
