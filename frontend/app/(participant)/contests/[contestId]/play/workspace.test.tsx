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

function freshInitialLog() {
  return { items: [], total: 0, failed: false };
}

// PlayHeader opens the events channel this workspace does not otherwise
// need for these tests; standing it in avoids a real EventSource and its
// own async churn.
vi.mock("./use-contest-events", () => ({
  useContestEvents: () => ({ offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "running" }),
}));
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh: vi.fn() }) }));

// Finding 5: counts how many times ResultPanel and SidePanel's own render
// functions actually run — not just whether their DOM survives, which
// reconciliation would preserve either way. Workspace wraps both in
// `React.memo`, so a render this counter did not see is exactly the proof a
// bottom-tab click, which changes nothing about either panel's own props,
// did not reach them.
const renderCounts = vi.hoisted(() => ({ result: 0, side: 0 }));
vi.mock("./result-panel", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./result-panel")>();
  return {
    ResultPanel: (props: Parameters<typeof actual.ResultPanel>[0]) => {
      renderCounts.result++;
      return actual.ResultPanel(props);
    },
  };
});
vi.mock("./side-panel", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./side-panel")>();
  return {
    SidePanel: (props: Parameters<typeof actual.SidePanel>[0]) => {
      renderCounts.side++;
      return actual.SidePanel(props);
    },
  };
});

function show() {
  return render(
    <Workspace
      contestId="c1"
      title="The Greenhouse Case"
      storyBody={<p>A body in the stacks.</p>}
      storyUnavailable={null}
      questionEntries={[]}
      initialLog={freshInitialLog()}
      locale="en"
      dict={en}
    />,
  );
}

/**
 * CodeMirror arrives through a dynamic `import()` (code-editor.tsx's own doc
 * comment says why); until it resolves, the console shows a plain, always-
 * typable fallback field in its place. A test about the *editor's own DOM
 * node* — not about typing, which works through either — waits for the real
 * one first, so the fallback→CodeMirror swap is not mistaken for whatever
 * the test is actually checking.
 *
 * Every test that types a query through the visible editor waits for it too
 * (finding 6): typing straight into `getByRole("textbox")` without waiting
 * only ever hit the fallback, because `userEvent.type` reliably outran the
 * dynamic import in a test environment — a false green that would not
 * survive the import taking one microtask longer. Defaults to `document.
 * body` so call sites that never captured a `container` still have
 * something to search.
 */
async function waitForRealEditor(container: HTMLElement = document.body) {
  await waitFor(() => expect(container.querySelector(".cm-editor")).toBeInTheDocument());
}

async function runQuery() {
  await waitForRealEditor();
  // `userEvent.type` does not reliably drive CodeMirror's contentEditable
  // div (it is not a text input and has no `selectionStart`/`selectionEnd`)
  // — click-then-keyboard is the pattern already proven against the real
  // editor elsewhere (code-editor.test.tsx, console.test.tsx).
  await userEvent.click(screen.getByRole("textbox"));
  await userEvent.keyboard("SELECT 1");
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
    const { container } = show();
    await waitForRealEditor(container);
    const editor = screen.getByRole("textbox");
    await userEvent.click(editor);
    await userEvent.keyboard("SELECT * FROM suspects");

    await userEvent.click(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.log }));
    await userEvent.click(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.story }));
    await userEvent.click(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.questions }));
    await userEvent.click(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.result }));

    // Not a form control any more (CodeMirror's content div), so the text is
    // read the way any other rendered content is, not through `.value`.
    expect(screen.getByRole("textbox")).toHaveTextContent("SELECT * FROM suspects");
  });

  test("switching a tab does not remount the editor's own DOM node", async () => {
    const { container } = show();
    await waitForRealEditor(container);
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

  // Finding 3: refreshing the log after every run spent one of the
  // participant's own `AdmitRead` units on a tab that, because a completed
  // run switches the workspace to "Result" in the same instant, was never
  // even the one showing. Running a query — refused or not — must not touch
  // the log endpoint at all while the participant is looking at the result.
  test("running a query does not refresh the query log", async () => {
    runResult.current = { kind: "refused", code: "query_too_often" };
    show();
    const before = logCalls.count;

    await runQuery();
    await waitFor(() => expect(screen.getByRole("status")).toBeInTheDocument());

    expect(logCalls.count).toBe(before);
  });

  // The log still has to catch up eventually — just on the moment a
  // participant actually goes to look at it, rather than on every query.
  test("switching to the log tab refreshes it", async () => {
    show();
    const before = logCalls.count;

    await userEvent.click(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.log }));

    await waitFor(() => expect(logCalls.count).toBeGreaterThan(before));
  });

  // Finding 5: a bottom-tab click sets state only in Workspace — none of
  // ResultPanel's or SidePanel's own props move because of it — so a
  // memoised panel should skip the render entirely rather than reconcile a
  // thousand-row table (or the whole side panel) for nothing.
  test("clicking a bottom tab does not re-render the memoised result and side panels", async () => {
    show();
    const resultRendersBefore = renderCounts.result;
    const sideRendersBefore = renderCounts.side;

    await userEvent.click(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.log }));
    await userEvent.click(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.result }));

    expect(renderCounts.result).toBe(resultRendersBefore);
    expect(renderCounts.side).toBe(sideRendersBefore);
  });
});
