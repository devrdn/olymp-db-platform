import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

import { Console } from "./console";
import type { ConsoleState } from "./actions";

// The action is the boundary: what it returns is what the console has to
// render, and everything behind it is tested where it lives.
const answer = vi.hoisted(() => ({ current: { kind: "idle" } as ConsoleState }));

vi.mock("./actions", () => ({
  runQueryAction: async () => answer.current,
}));

function show() {
  return render(<Console contestId="c1" dict={en} />);
}

async function run() {
  await userEvent.type(screen.getByRole("textbox"), "SELECT 1");
  await userEvent.click(screen.getByRole("button", { name: en.participant.console.run }));
}

describe("the SQL console", () => {
  test("shows the rows it was given", async () => {
    answer.current = {
      kind: "answer",
      result: { columns: ["id", "note"], rows: [["1", "a knife"]], truncated: false, rows_affected: 0 },
    };
    show();
    await run();

    const table = screen.getByRole("table");
    expect(within(table).getByText("a knife")).toBeInTheDocument();
    expect(within(table).getByRole("columnheader", { name: "note" })).toBeInTheDocument();
  });

  // NULL and the empty string are different things in SQL, and a participant
  // debugging a left join is looking at exactly that column.
  test("shows a null as a null rather than as an empty cell", async () => {
    answer.current = {
      kind: "answer",
      result: { columns: ["alibi"], rows: [[null]], truncated: false, rows_affected: 0 },
    };
    show();
    await run();

    expect(within(screen.getByRole("table")).getByText(en.participant.console.null)).toBeInTheDocument();
  });

  // Nine hundred rows of nine thousand, unannounced, is a wrong answer rather
  // than a short one.
  test("says when the answer is longer than what is shown", async () => {
    answer.current = {
      kind: "answer",
      result: { columns: ["g"], rows: [["1"]], truncated: true, rows_affected: 0 },
    };
    show();
    await run();

    expect(screen.getByText(/longer than that/i)).toBeInTheDocument();
  });

  test("a write answers with a count rather than an empty table", async () => {
    answer.current = {
      kind: "answer",
      result: { columns: [], rows: [], truncated: false, rows_affected: 3 },
    };
    show();
    await run();

    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    expect(screen.getByText(/3 rows changed/i)).toBeInTheDocument();
  });

  // "That function is not available" without naming it is the same
  // unactionable answer whichever language it is in.
  test("a refusal names what it was about", async () => {
    answer.current = { kind: "refused", code: "query_function_not_supported", subject: "pg_sleep" };
    show();
    await run();

    const status = screen.getByRole("status");
    expect(status).toHaveTextContent(en.errors.query_function_not_supported);
    expect(status).toHaveTextContent("pg_sleep");
  });

  // A table left under a red box invites reading yesterday's rows as the
  // answer to today's question.
  test("a refusal replaces the previous answer rather than sitting above it", async () => {
    answer.current = {
      kind: "answer",
      result: { columns: ["a"], rows: [["1"]], truncated: false, rows_affected: 0 },
    };
    show();
    await run();
    expect(screen.getByRole("table")).toBeInTheDocument();

    answer.current = { kind: "refused", code: "query_too_often" };
    await run();

    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  test("an unrecognised code still says something", async () => {
    answer.current = { kind: "refused", code: "something_from_the_future" };
    show();
    await run();

    expect(screen.getByRole("status")).toHaveTextContent(en.errors.fallback);
  });
});
