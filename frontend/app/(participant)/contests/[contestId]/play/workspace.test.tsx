import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

import { Workspace } from "./workspace";
import type { AnswerState, ConsoleState, QuestionsRefreshResult, QueryLogRefreshResult } from "./actions";

const runResult = vi.hoisted(() => ({ current: { kind: "idle" } as ConsoleState }));
const logCalls = vi.hoisted(() => ({ count: 0 }));

vi.mock("./actions", () => ({
  runQueryAction: async () => runResult.current,
  submitAnswerAction: async () => ({ kind: "idle" }) as AnswerState,
  refreshQuestionsAction: async () => ({ kind: "ok", items: [] }) as QuestionsRefreshResult,
  fetchQueryLogAction: async (): Promise<QueryLogRefreshResult> => {
    logCalls.count++;
    return { kind: "ok", items: [], total: 0 };
  },
}));

// PlayHeader opens the events channel this workspace does not otherwise
// need for these tests; standing it in avoids a real EventSource and its
// own async churn.
vi.mock("./use-contest-events", () => ({
  useContestEvents: () => ({ offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "running" }),
}));
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh: vi.fn() }) }));

function show() {
  return render(
    <Workspace
      contestId="c1"
      title="The Greenhouse Case"
      storyBody={<p>A body in the stacks.</p>}
      storyUnavailable={null}
      questionEntries={[]}
      initialLog={{ items: [], total: 0 }}
      locale="en"
      dict={en}
    />,
  );
}

async function runQuery() {
  await userEvent.type(screen.getByRole("textbox"), "SELECT 1");
  await userEvent.click(screen.getByRole("button", { name: en.participant.console.run }));
}

describe("the play workspace", () => {
  test("shows the console, the bottom tabs and the side tabs together", () => {
    show();

    expect(screen.getByRole("textbox")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.result })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.log })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.story })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.questions })).toBeInTheDocument();
  });

  // The plan's central requirement: switching a tab must not remount or
  // refetch anything, and a half-typed query has to survive it.
  test("a half-typed query survives switching every tab and back", async () => {
    show();
    const editor = screen.getByRole("textbox");
    await userEvent.type(editor, "SELECT * FROM suspects");

    await userEvent.click(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.log }));
    await userEvent.click(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.story }));
    await userEvent.click(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.questions }));
    await userEvent.click(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.result }));

    expect(screen.getByRole("textbox")).toHaveValue("SELECT * FROM suspects");
  });

  test("switching a tab does not remount the editor's own DOM node", async () => {
    show();
    const editor = screen.getByRole("textbox");

    await userEvent.click(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.log }));

    expect(screen.getByRole("textbox")).toBe(editor);
  });

  // Seeing what a query just did is the point of running it — a completed
  // run switches the bottom panel to "Result" by itself, the way an editor's
  // own output panel opens itself.
  test("a completed run switches the bottom panel to Result on its own", async () => {
    runResult.current = {
      kind: "answer",
      result: { columns: ["id"], rows: [["1"]], truncated: false, rows_affected: 0 },
    };
    show();
    await userEvent.click(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.log }));
    await runQuery();

    await waitFor(() => expect(screen.getByRole("table")).toBeInTheDocument());
    expect(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.result })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });

  // A run that reaches the server, refused or not, may have written a log
  // row — the log tab refetches rather than going stale until the
  // participant happens to reload.
  test("a completed run refreshes the query log", async () => {
    runResult.current = { kind: "refused", code: "query_too_often" };
    show();
    const before = logCalls.count;

    await runQuery();

    await waitFor(() => expect(logCalls.count).toBeGreaterThan(before));
  });
});
