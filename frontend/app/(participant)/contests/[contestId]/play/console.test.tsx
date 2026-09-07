import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

import { ConsoleEditor } from "./console";
import type { ConsoleState } from "./actions";

// The action is the boundary: what it returns is what this component has to
// report through onResult, and what a caller does with it (ResultPanel,
// the query log's own refresh) is tested where that lives.
const answer = vi.hoisted(() => ({ current: { kind: "idle" } as ConsoleState }));
const runQueryAction = vi.hoisted(() => vi.fn(async () => answer.current));

vi.mock("./actions", () => ({ runQueryAction }));

async function run(onResult: (state: ConsoleState) => void) {
  render(<ConsoleEditor contestId="c1" dict={en} onResult={onResult} />);
  await userEvent.type(screen.getByRole("textbox"), "SELECT 1");
  await userEvent.click(screen.getByRole("button", { name: en.participant.console.run }));
}

describe("the SQL editor", () => {
  test("reports every completed run through onResult", async () => {
    answer.current = {
      kind: "answer",
      result: { columns: ["id"], rows: [["1"]], truncated: false, rows_affected: 0 },
    };
    const onResult = vi.fn();
    await run(onResult);

    // Once for the initial idle state, once for the completed run.
    expect(onResult).toHaveBeenLastCalledWith(answer.current);
  });

  test("reports a refusal the same way it reports an answer", async () => {
    answer.current = { kind: "refused", code: "query_too_often" };
    const onResult = vi.fn();
    await run(onResult);

    expect(onResult).toHaveBeenLastCalledWith(answer.current);
  });

  test("shows nothing about the previous result — that lives in ResultPanel now", async () => {
    answer.current = {
      kind: "answer",
      result: { columns: ["id"], rows: [["1"]], truncated: false, rows_affected: 0 },
    };
    await run(vi.fn());

    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  test("disables the run button while a query is in flight", async () => {
    let resolve: (value: ConsoleState) => void = () => {};
    runQueryAction.mockImplementationOnce(() => new Promise((r) => { resolve = r; }));
    render(<ConsoleEditor contestId="c1" dict={en} onResult={vi.fn()} />);
    await userEvent.type(screen.getByRole("textbox"), "SELECT 1");
    await userEvent.click(screen.getByRole("button", { name: en.participant.console.run }));

    expect(screen.getByRole("button", { name: en.participant.console.running })).toBeDisabled();
    resolve({ kind: "idle" });
  });
});
