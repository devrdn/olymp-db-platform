import { existsSync, readFileSync, statSync } from "node:fs";
import path from "node:path";

import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import { offeredCompletions, offersAfterTyping } from "@/components/product/completion-test-support";
import { answerYesOnFakeClock, flushFakeClock, onFakeClock } from "@/components/ui/confirm-dialog-test-support";
import { expectTouchTarget } from "@/components/ui/touch-target-test-support";
import en from "@/lib/i18n/dictionaries/en";
import { fill } from "@/lib/i18n/fill";

import { PanelToggles, PanelVisibilityProvider } from "./panel-toggles";
import { PrintView } from "./print-view";
import type { QuestionEntry } from "./questions-panel";
import { StoryCover } from "./story-cover";
import { Workspace } from "./workspace";
import type {
  AnswerState,
  ConsoleState,
  QuestionsRefreshResult,
  QueryLogRefreshResult,
  ResetState,
  SchemaRefreshResult,
} from "./requests";

const runResult = vi.hoisted(() => ({ current: { kind: "idle" } as ConsoleState }));
/** Holds a run in flight until the test lets it settle; null settles at once. */
const runGate = vi.hoisted(() => ({ current: null as Promise<void> | null }));
/** What the questions to the organisers read, which a test may hold back. */
const clarificationsRead = vi.hoisted(() => ({ current: null as Promise<unknown> | null }));
const logCalls = vi.hoisted(() => ({ count: 0 }));
const schemaReads = vi.hoisted(() => ({ count: 0, next: { kind: "refused", code: "unreachable" } as SchemaRefreshResult }));

vi.mock("./requests", () => ({
  runQuery: async () => {
    if (runGate.current) await runGate.current;
    return runResult.current;
  },
  submitAnswer: async () => ({ kind: "idle" }) as AnswerState,
  refreshQuestions: async () => ({ kind: "ok", items: [] }) as QuestionsRefreshResult,
  fetchQueryLog: async (): Promise<QueryLogRefreshResult> => {
    logCalls.count++;
    return { kind: "ok", items: [], total: 0 };
  },
  resetDatabase: async (): Promise<ResetState> => ({ kind: "done" }),
  fetchClarifications: async () =>
    clarificationsRead.current ?? { kind: "ok", feed: { items: [], next: null, open: 0 } },
  refreshSchema: async (): Promise<SchemaRefreshResult> => {
    schemaReads.count++;
    return schemaReads.next;
  },
}));

// The log's three-second refresh gate is tested in its own panel; here it
// would make a collapse and expand one keystroke apart prove nothing.
vi.mock("@/lib/api/querylog-terms", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/querylog-terms")>()),
  QUERY_LOG_REFRESH_MIN_INTERVAL_MS: 0,
}));

function freshInitialLog() {
  return { items: [], total: 0, failed: false };
}

// The notes and SQL tabs save straight to the API as soon as anything is
// typed. Answering successfully keeps a retry timer from outliving a test.
beforeEach(() => {
  window.localStorage.clear();
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify({ updated_at: "v1" }), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
    ),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

// The events channel belongs to `PlayHeader`, outside this component; the
// stand-ins keep a reintroduced `EventSource` from making these flaky.
vi.mock("./use-contest-events", () => ({
  useContestEvents: () => ({ offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "running" }),
}));
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh: vi.fn() }) }));

// Counts real renders of the memoised panels, which DOM survival alone
// cannot show.
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

const A_SCHEMA = {
  truncated: false,
  tables: [{ name: "guests", columns: [{ name: "id", type: "uuid", nullable: false, references: "" }] }],
};

// The print copy's text differs from `storyBody` so `getByText` is never
// ambiguous: both stay mounted, and jsdom ignores the class hiding one.
function show(
  schema: typeof A_SCHEMA | null = null,
  overrides: {
    storyMarkdown?: string | null;
    storyUnavailable?: string | null;
    schemaNotes?: React.ReactNode;
    workspace?: typeof A_WORKSPACE | null;
    sqlMode?: "read_only" | "read_write";
    resultDownload?: boolean;
    schemaDiagram?: boolean;
  } = {},
) {
  return render(
    <Workspace
      accountId="u1"
      contestId="c1"
      storyBody={<p>A body in the stacks.</p>}
      storyCover={
        <StoryCover
          contestId="c1"
          title="The Greenhouse Case"
          coverHash="9f86d081884c7d65"
          coverAttribution="Photo: A. Organiser, CC BY 4.0"
          dict={en}
        />
      }
      printView={printCopy("storyMarkdown" in overrides ? (overrides.storyMarkdown ?? null) : "The printed case notes.")}
      storyUnavailable={overrides.storyUnavailable ?? null}
      schemaNotes={overrides.schemaNotes ?? null}
      questionEntries={[]}
      schema={schema}
      sqlMode={overrides.sqlMode ?? "read_only"}
      resultDownload={overrides.resultDownload ?? true}
      schemaDiagram={overrides.schemaDiagram ?? true}
      initialLog={freshInitialLog()}
      workspace={"workspace" in overrides ? (overrides.workspace ?? null) : A_WORKSPACE}
      locale="en"
      dict={en}
    />,
  );
}

const A_WORKSPACE = {
  notes: { body: "the gardener lied", updatedAt: "2026-09-17T10:00:00Z" },
  tabs: [{ id: "t1", title: "Query 1", body: "", position: 0, updatedAt: "2026-09-17T10:00:00Z" }],
};

/**
 * What `page.tsx` hands `Workspace` as `printView`, rendered here so the
 * assertions read the real print output.
 */
function printCopy(storyMarkdown: string | null) {
  if (storyMarkdown === null) return null;
  return (
    <PrintView
      contestTitle="The Greenhouse Case"
      participantName="Ada Lovelace"
      date="8 Sep 2026"
      storyMarkdown={storyMarkdown}
      dict={en}
    />
  );
}

/** The print-only wrapper `workspace.tsx` renders. */
function printOnlyContainer(container: HTMLElement): HTMLElement | null {
  return (
    [...container.querySelectorAll("div")].find(
      (div) => div.classList.contains("hidden") && div.classList.contains("print:block"),
    ) ?? null
  );
}

/**
 * Waits for CodeMirror, which loads through a dynamic `import()` behind a
 * plain fallback field. Tests about the editor's node, or typing into it,
 * wait first; `userEvent` otherwise outruns the import and only ever types
 * into the fallback.
 */
async function waitForRealEditor(container: HTMLElement = document.body) {
  await waitFor(() => {
    expect(container.querySelector(".cm-editor")).toBeInTheDocument();
    // React removes the fallback a commit later; until then the label names two fields.
    expect(within(container).getAllByRole("textbox", { name: en.participant.console.label })).toHaveLength(1);
  });
}

/** The name of a table in the schema panel, the button that writes a query on it. */
function tableName(table: string) {
  return { name: fill(en.participant.play.schema.query, { table }) };
}

/**
 * Leaves the focus on `<body>`, as a browser does when the focused control
 * is disabled or hidden. jsdom does neither by itself.
 */
function dropFocus() {
  const holder = document.createElement("button");
  document.body.append(holder);
  holder.focus();
  holder.remove();
  expect(document.body).toHaveFocus();
}

async function runQuery() {
  await waitForRealEditor();
  // `userEvent.type` cannot drive CodeMirror's contentEditable; click then
  // keyboard is the pattern code-editor.test.tsx uses.
  await userEvent.click(screen.getByRole("textbox"));
  await userEvent.keyboard("SELECT 1");
  await userEvent.click(screen.getByRole("button", { name: en.participant.console.run }));
}

describe("the play workspace", () => {
  test("shows the console, the bottom tabs and the side tabs together", () => {
    show();

    expect(screen.getByRole("textbox")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: en.participant.play.workspace.tabs.result })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: en.participant.play.workspace.tabs.log })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.story })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.questions })).toBeInTheDocument();
  });

  // Beside Run in the toolbar, at its height on a touch screen. A tap is
  // hit-tested against a pill's rounded ends, so a square layer covers them.
  test("the result and log buttons are 44px high on a touch screen, corners included", () => {
    show();

    for (const name of [en.participant.play.workspace.tabs.result, en.participant.play.workspace.tabs.log]) {
      expectTouchTarget(
        screen.getByRole("button", { name }),
        "pointer-coarse:min-h-11",
        "pointer-coarse:relative",
        "pointer-coarse:after:absolute",
        "pointer-coarse:after:inset-0",
      );
    }
  });

  test("offers the table descriptions on their own tab only when the page has them", () => {
    const { unmount } = show();
    expect(screen.queryByRole("tab", { name: en.participant.play.workspace.tabs.tables })).not.toBeInTheDocument();
    unmount();

    show(null, { schemaNotes: <p>The suspects table.</p> });
    expect(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.tables })).toBeInTheDocument();
    expect(screen.getByText("The suspects table.")).toBeInTheDocument();
  });

  test("a half-typed query survives switching every tab and back", async () => {
    const { container } = show();
    await waitForRealEditor(container);
    const editor = screen.getByRole("textbox");
    await userEvent.click(editor);
    await userEvent.keyboard("SELECT * FROM suspects");

    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.tabs.log }));
    await userEvent.click(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.story }));
    await userEvent.click(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.questions }));
    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.tabs.result }));

    // CodeMirror's content is not a form control, so read its text.
    expect(screen.getByRole("textbox")).toHaveTextContent("SELECT * FROM suspects");
  });

  test("switching a tab does not remount the editor's own DOM node", async () => {
    const { container } = show();
    await waitForRealEditor(container);
    const editor = screen.getByRole("textbox");

    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.tabs.log }));

    expect(screen.getByRole("textbox")).toBe(editor);
  });

  test("a completed run switches the bottom panel to Result on its own", async () => {
    runResult.current = {
      kind: "answer",
      result: { columns: ["id"], rows: [["1"]], truncated: false, rows_affected: 0 },
    };
    show();
    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.tabs.log }));
    await runQuery();

    await waitFor(() => expect(screen.getByRole("table")).toBeInTheDocument());
    expect(screen.getByRole("button", { name: en.participant.play.workspace.tabs.result })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  test.each([
    [true, 1],
    [false, 0],
  ])("a contest whose result download is %s shows %i download buttons under a result", async (resultDownload, count) => {
    runResult.current = {
      kind: "answer",
      result: { columns: ["id"], rows: [["1"]], truncated: false, rows_affected: 0 },
    };
    show(null, { resultDownload });
    await runQuery();

    await waitFor(() => expect(screen.getByRole("table")).toBeInTheDocument());
    expect(screen.queryAllByRole("button", { name: en.participant.play.workspace.download })).toHaveLength(count);
  });

  // The log's reads spend the play screen's read budget, and a run switches
  // the bottom panel to "Result" anyway.
  test("running a query does not refresh the query log", async () => {
    runResult.current = { kind: "refused", code: "query_too_often" };
    show();
    const before = logCalls.count;

    await runQuery();
    // The refusal itself: the SQL tabs' save status is a live region too.
    await waitFor(() => expect(screen.getByText(en.errors.query_too_often)).toBeInTheDocument());

    expect(logCalls.count).toBe(before);
  });

  test("switching to the log tab refreshes it", async () => {
    show();
    const before = logCalls.count;

    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.tabs.log }));

    await waitFor(() => expect(logCalls.count).toBeGreaterThan(before));
  });

  test("clicking a bottom tab does not re-render the memoised result and side panels", async () => {
    show();
    const resultRendersBefore = renderCounts.result;
    const sideRendersBefore = renderCounts.side;

    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.tabs.log }));
    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.tabs.result }));

    expect(renderCounts.result).toBe(resultRendersBefore);
    expect(renderCounts.side).toBe(sideRendersBefore);
  });
});

// Proves the collector is mounted with the screen, not what it does
// (use-signals.ts).
describe("the browser signals", () => {
  test("a paste into the notes is sent for this contest, re-rendering nothing", async () => {
    show();
    const field = screen.getByRole("textbox", { name: en.participant.play.workspace.notes.label, hidden: true });
    const sideRendersBefore = renderCounts.side;

    const paste = new Event("paste", { bubbles: true, cancelable: true });
    Object.defineProperty(paste, "clipboardData", { value: { getData: () => "the key" } });
    field.dispatchEvent(paste);
    window.dispatchEvent(new Event("pagehide"));

    const fetchMock = vi.mocked(fetch);
    const signal = fetchMock.mock.calls.find(([url]) => String(url).endsWith("/contests/c1/play/signals"));
    expect(signal).toBeDefined();
    expect(JSON.parse(String(signal?.[1]?.body)).events).toMatchObject([{ kind: "paste", target: "notes", text: "the key" }]);
    expect(renderCounts.side).toBe(sideRendersBefore);
  });
});

describe("the notes", () => {
  test("open with what the page read", () => {
    show();

    expect(
      screen.getByRole("textbox", { name: en.participant.play.workspace.notes.label, hidden: true }),
    ).toHaveValue("the gardener lied");
  });

  test("say they could not be loaded when the page could not read them", () => {
    show(null, { workspace: null });

    expect(screen.getByText(en.participant.play.workspace.notes.failed)).toBeInTheDocument();
  });

  // Typing is the hot path: nothing above the notes may render per keystroke.
  test("typing in them does not re-render the side panel", async () => {
    show();
    await userEvent.click(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.notes }));
    const field = screen.getByRole("textbox", { name: en.participant.play.workspace.notes.label });
    const sideRendersBefore = renderCounts.side;

    fireEvent.change(field, { target: { value: "the gardener lied twice" } });
    fireEvent.change(field, { target: { value: "the gardener lied three times" } });

    expect(renderCounts.side).toBe(sideRendersBefore);
  });
});

// jsdom cannot evaluate `@media print`, so these check the DOM half: the
// print-only copy's content and the class that removes the interactive
// workspace from the page.
describe("the print-only copy of the story", () => {
  test("is in the tree, holding the contest title, the byline and the story", () => {
    const { container } = show();

    const printOnly = printOnlyContainer(container);
    expect(printOnly).not.toBeNull();
    expect(printOnly!.textContent).toContain("The Greenhouse Case");
    expect(printOnly!.textContent).toContain("Ada Lovelace");
    expect(printOnly!.textContent).toContain("8 Sep 2026");
    expect(printOnly!.textContent).toContain("The printed case notes.");
  });

  test("carries nothing when there is no story to print", () => {
    const { container } = show(null, { storyMarkdown: null, storyUnavailable: "This contest has no story yet" });

    const printOnly = printOnlyContainer(container);
    expect(printOnly).not.toBeNull();
    expect(printOnly!.textContent).toBe("");
  });

  /** SPEC.md §10: the cover is for the screen; a printed story is text alone. */
  test("carries no cover: a printed story is text, not atmosphere", () => {
    const { container } = show();

    const printOnly = printOnlyContainer(container);
    expect(printOnly!.querySelector("img")).toBeNull();
    // Present on screen, so its absence above is a decision.
    expect(container.querySelector("img[alt='Cover of The Greenhouse Case']")).not.toBeNull();
  });

  test("the interactive workspace is marked to disappear under print", () => {
    const { container } = show();

    // Workspace renders two top-level siblings, the print copy and the
    // workspace, so the one that is not the print copy is the workspace.
    // A class-based `closest()` would match nested console markup.
    const printOnly = printOnlyContainer(container);
    const workspaceRoot = [...container.children].find((el) => el !== printOnly) as HTMLElement | undefined;
    expect(workspaceRoot).not.toBeUndefined();
    expect(workspaceRoot!.className).toMatch(/(^|\s)print:hidden(\s|$)/);
  });
});

// Absent rather than empty when the catalogues are closed: the panel would
// be the very oracle being hidden.
describe("the schema column", () => {
  test("is there when the contest shows its schema", () => {
    show(A_SCHEMA);

    expect(screen.getByRole("region", { name: en.participant.play.schema.heading })).toBeInTheDocument();
    expect(screen.getByRole("button", tableName("guests"))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: en.participant.play.schema.diagram.openLabel })).toBeInTheDocument();
  });

  test("keeps the tables but not the diagram in a contest that switched the diagram off", () => {
    show(A_SCHEMA, { schemaDiagram: false });

    expect(screen.getByRole("button", tableName("guests"))).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: en.participant.play.schema.diagram.openLabel }),
    ).not.toBeInTheDocument();
  });

  test("is absent entirely in a contest that hides it, and so is the diagram", () => {
    show(null);

    expect(
      screen.queryByRole("region", { name: en.participant.play.schema.heading }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: en.participant.play.schema.diagram.openLabel, hidden: true }),
    ).not.toBeInTheDocument();
  });
});

/**
 * A click on a table's name writes a query on it into the SQL tab being
 * edited, for the participant to change or run.
 */
describe("a click on a table's name", () => {
  const TWO_TABS = {
    notes: A_WORKSPACE.notes,
    tabs: [
      { id: "t1", title: "Query 1", body: "SELECT 1", position: 0, updatedAt: "2026-09-17T10:00:00Z" },
      { id: "t2", title: "Query 2", body: "", position: 1, updatedAt: "2026-09-17T10:00:00Z" },
    ],
  };

  function lines(container: HTMLElement) {
    return [...container.querySelectorAll(".cm-line")].map((line) => line.textContent);
  }

  test("writes the query into the editor and the field a run sends, without running it", async () => {
    runResult.current = { kind: "refused", code: "query_too_often" };
    const { container } = show(A_SCHEMA, { workspace: TWO_TABS });
    await waitForRealEditor(container);

    await userEvent.click(screen.getByRole("button", tableName("guests")));

    expect(lines(container)).toEqual(["SELECT 1", "SELECT * FROM guests LIMIT 5;"]);
    expect(container.querySelector<HTMLTextAreaElement>('textarea[name="sql"]')?.value).toBe(
      "SELECT 1\nSELECT * FROM guests LIMIT 5;",
    );
    expect(screen.getByText(en.participant.play.workspace.resultEmpty)).toBeInTheDocument();
    expect(screen.queryByText(en.errors.query_too_often)).not.toBeInTheDocument();
  });

  test("writes into the tab that is open, and no other", async () => {
    const { container } = show(A_SCHEMA, { workspace: TWO_TABS });
    await waitForRealEditor(container);

    await userEvent.click(screen.getByRole("tab", { name: "Query 2" }));
    await userEvent.click(screen.getByRole("button", tableName("guests")));
    expect(lines(container)).toEqual(["SELECT * FROM guests LIMIT 5;"]);

    await userEvent.click(screen.getByRole("tab", { name: "Query 1" }));
    expect(lines(container)).toEqual(["SELECT 1"]);
  });

  test("quotes a table name the database would otherwise misread", async () => {
    const { container } = show(
      { truncated: false, tables: [{ name: "Order", columns: [] }] },
      { workspace: TWO_TABS },
    );
    await waitForRealEditor(container);
    await userEvent.click(screen.getByRole("tab", { name: "Query 2" }));

    await userEvent.click(screen.getByRole("button", tableName("Order")));

    expect(lines(container)).toEqual(['SELECT * FROM "Order" LIMIT 5;']);
  });

  test("works from the keyboard, and leaves the caret in the editor", async () => {
    const { container } = show(A_SCHEMA, { workspace: TWO_TABS });
    await waitForRealEditor(container);
    await userEvent.click(screen.getByRole("tab", { name: "Query 2" }));

    screen.getByRole("button", tableName("guests")).focus();
    await userEvent.keyboard("{Enter}");

    expect(lines(container)).toEqual(["SELECT * FROM guests LIMIT 5;"]);
    expect(screen.getByRole("textbox", { name: en.participant.console.label })).toHaveFocus();
  });
});

describe("the database reset", () => {
  const r = en.participant.play.workspace.reset;

  beforeEach(() => {
    schemaReads.count = 0;
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  /** Resets on a fake clock, which it leaves for the real one before the test goes on. */
  async function reset() {
    await onFakeClock(async () => {
      fireEvent.click(screen.getByRole("button", { name: r.open }));
      await answerYesOnFakeClock(r.confirm);
      expect(screen.getByRole("status")).toBeInTheDocument();
      // The open dialog hides the screen behind it from the accessibility tree.
      fireEvent.click(screen.getAllByRole("button", { name: r.close }).at(-1)!);
      await flushFakeClock();
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
  }

  test("is offered by a contest that lets participants write", () => {
    show(A_SCHEMA, { sqlMode: "read_write" });

    expect(screen.getByRole("button", { name: r.open })).toBeInTheDocument();
  });

  test("is not offered by a read-only contest", () => {
    show(A_SCHEMA, { sqlMode: "read_only" });

    expect(screen.queryByRole("button", { name: r.open })).not.toBeInTheDocument();
  });

  test("redraws the schema panel with the schema read after the reset", async () => {
    schemaReads.next = {
      kind: "ok",
      schema: { truncated: false, tables: [{ name: "suspects", columns: [] }] },
    };
    show(A_SCHEMA, { sqlMode: "read_write" });

    await reset();

    expect(await screen.findByRole("button", tableName("suspects"))).toBeInTheDocument();
    expect(screen.queryByRole("button", tableName("guests"))).not.toBeInTheDocument();
  });

  test("offers the tables of the schema read after the reset as completions", async () => {
    schemaReads.next = {
      kind: "ok",
      schema: { truncated: false, tables: [{ name: "suspects", columns: [] }] },
    };
    show(A_SCHEMA, { sqlMode: "read_write" });
    await reset();
    await screen.findByRole("button", tableName("suspects"));

    await waitForRealEditor();
    vi.useFakeTimers({ shouldAdvanceTime: true });
    await userEvent.click(screen.getByRole("textbox", { name: en.participant.console.label }));
    await userEvent.keyboard("SELECT * FROM sus");

    await offersAfterTyping("suspects");
  });

  // The invariant is the server's; this checks the screen adds no way round it.
  test("offers no table as a completion in a contest that hides its schema, even after a reset", async () => {
    schemaReads.next = {
      kind: "ok",
      schema: { truncated: false, tables: [{ name: "selfies", columns: [] }] },
    };
    show(null, { sqlMode: "read_write" });
    await reset();

    await waitForRealEditor();
    vi.useFakeTimers({ shouldAdvanceTime: true });
    await userEvent.click(screen.getByRole("textbox", { name: en.participant.console.label }));
    await userEvent.keyboard("SEL");
    await offersAfterTyping("SELECT");

    expect(offeredCompletions()).not.toContain("selfies");
    expect(schemaReads.count).toBe(0);
  });

  test("keeps the schema on screen when it cannot be read again", async () => {
    schemaReads.next = { kind: "refused", code: "play_read_too_often" };
    show(A_SCHEMA, { sqlMode: "read_write" });

    await reset();

    await waitFor(() => expect(schemaReads.count).toBe(1));
    expect(screen.getByRole("button", tableName("guests"))).toBeInTheDocument();
  });

  test("clears the last result, whose rows came from the database before it", async () => {
    runResult.current = {
      kind: "answer",
      result: { columns: ["id"], rows: [["1"]], truncated: false, rows_affected: 0 },
    };
    show(null, { sqlMode: "read_write" });
    await runQuery();
    await waitFor(() => expect(screen.getByRole("table")).toBeInTheDocument());

    await reset();

    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    expect(screen.getByText(en.participant.play.workspace.resultEmpty)).toBeInTheDocument();
  });

  test("asks for no schema in a contest that hides it", async () => {
    show(null, { sqlMode: "read_write" });

    await reset();

    expect(schemaReads.count).toBe(0);
  });
});

/**
 * jsdom lays nothing out, so these assert the rules the widths come from:
 * where each pane is placed, and whether a track may exceed its container.
 */
describe("the pane grid's own shape", () => {
  function paneGrid(container: HTMLElement): HTMLElement {
    const grid = container.querySelector<HTMLElement>('[style*="--pane-schema"]');
    if (!grid) throw new Error("the pane grid was not found");
    return grid;
  }

  /**
   * The `order` a pane declares for a breakpoint range, most specific prefix
   * first, or null where it would default to 0 and be placed before every
   * pane that declares one.
   */
  function declaredOrder(el: Element, prefixes: readonly string[]): number | null {
    for (const prefix of prefixes) {
      for (const cls of el.classList) {
        const match = new RegExp(`^${prefix}order-(\\d+)$`).exec(cls);
        if (match) return Number(match[1]);
      }
    }
    return null;
  }

  /** The panes laid out in one range: those not hidden there. */
  function placedPanes(grid: HTMLElement, hiddenClass: string): Element[] {
    return [...grid.children].filter((child) => !child.classList.contains(hiddenClass));
  }

  // A single unordered pane is enough for grid auto-placement to reorder the
  // row and squeeze the console into a 1px divider column.
  test("every pane placed between the two breakpoints declares its own order, and no two share one", () => {
    const { container } = show(A_SCHEMA);
    const grid = paneGrid(container);

    // 47.5rem to 64rem: the schema handle is hidden, the other four are laid out.
    const orders = placedPanes(grid, "max-wide:hidden").map((pane) =>
      declaredOrder(pane, ["narrow:max-wide:", "max-wide:"]),
    );

    expect(orders).not.toContain(null);
    expect(new Set(orders).size).toBe(orders.length);
  });

  // An implicit `auto` track grows to the result table's max-content width,
  // which sized the editor by the table and scrolled the page sideways.
  test("the console column declares a track that cannot exceed its container", () => {
    const { container } = show(A_SCHEMA);
    const column = container.querySelector("form")?.closest("div.grid");

    expect(column).not.toBeNull();
    expect(column!.className).toMatch(/(^|\s)grid-cols-1(\s|$)/);
  });

  // Below the narrow breakpoint one pane is laid out at a time. An implicit
  // `auto` row would size it by its content, and a long result or story would
  // scroll the page instead of its own box.
  test("below the narrow breakpoint the grid and the console column each declare one row that fills the height", () => {
    const { container } = show(A_SCHEMA);
    const grid = paneGrid(container);
    const column = container.querySelector("form")?.closest("div.grid");

    expect(grid).toHaveClass("max-narrow:grid-rows-[minmax(0,1fr)]");
    expect(column).toHaveClass("max-narrow:grid-rows-[minmax(0,1fr)]");
  });
});

// What `Workspace` pulls into the browser, which no render can show. As a
// client component, everything it imports ships to the client, and
// `PrintView` reaches `react-markdown` (about 32 KiB gzipped). It is rendered
// on the server instead (page.tsx).
describe("what Workspace pulls into the client bundle", () => {
  /** Bare package specifiers reachable from `entry`, following this app's files (relative and `@/`). */
  function packagesReachableFrom(entry: string): Set<string> {
    const root = path.resolve(__dirname, "../../../../..");
    const packages = new Set<string>();
    const seen = new Set<string>();

    const resolve = (specifier: string, from: string): string | null => {
      const base = specifier.startsWith("@/")
        ? path.join(root, specifier.slice(2))
        : path.resolve(path.dirname(from), specifier);
      for (const candidate of [
        base,
        `${base}.ts`,
        `${base}.tsx`,
        path.join(base, "index.ts"),
        path.join(base, "index.tsx"),
      ]) {
        if (existsSync(candidate) && statSync(candidate).isFile()) return candidate;
      }
      return null;
    };

    const walk = (file: string) => {
      if (seen.has(file)) return;
      seen.add(file);
      const source = readFileSync(file, "utf8");
      // Type-only imports are erased and reach no bundle.
      const imports = source.matchAll(/(?:^|\n)\s*(?:import|export)\s+(?!type\s)(?:[^'"\n]*?\sfrom\s+)?["']([^"']+)["']/g);
      for (const [, specifier] of imports) {
        if (specifier.startsWith(".") || specifier.startsWith("@/")) {
          const resolved = resolve(specifier, file);
          if (resolved) walk(resolved);
          continue;
        }
        packages.add(specifier);
      }
    };

    walk(path.resolve(__dirname, entry));
    return packages;
  }

  test("not the Markdown parser: the story and its print copy are rendered on the server", () => {
    const packages = packagesReachableFrom("workspace.tsx");

    // If the walk resolved nothing, an empty set would pass for the wrong
    // reason.
    expect(packages.has("react")).toBe(true);

    expect([...packages].filter((name) => name === "react-markdown" || name === "remark-gfm")).toEqual([]);
  });

  // Without this, deleting the print copy outright would also pass the test
  // above.
  test("page.tsx still renders the print copy, on the server, where the parser is free", () => {
    expect(packagesReachableFrom("page.tsx").has("react-markdown")).toBe(true);
    expect(readFileSync(path.resolve(__dirname, "page.tsx"), "utf8")).toContain("<PrintView");
  });
});

/**
 * SPEC.md §5: a collapsed panel leaves the grid with its divider, so the editor takes
 * its room. The toggles live in the header, so these render both inside one
 * provider, as `page.tsx` does.
 */
describe("collapsing a panel", () => {
  const p = en.participant.play.workspace.panels;
  const panes = en.participant.play.workspace.panes;

  /** One contest per test: what is collapsed is remembered per contest. */
  let contests = 0;
  function showWithToggles(
    schema: typeof A_SCHEMA | null = A_SCHEMA,
    questionEntries: QuestionEntry[] = [],
  ) {
    contests += 1;
    const contestId = `collapse-${contests}`;
    const view = render(
      <PanelVisibilityProvider contestId={contestId}>
        <PanelToggles dict={en} />
        <Workspace
          accountId="u1"
          contestId={contestId}
          storyBody={<p>A body in the stacks.</p>}
          storyCover={null}
          printView={null}
          storyUnavailable={null}
          questionEntries={questionEntries}
          schema={schema}
          sqlMode="read_only"
          resultDownload
          schemaDiagram
          initialLog={freshInitialLog()}
          workspace={A_WORKSPACE}
          locale="en"
          dict={en}
        />
      </PanelVisibilityProvider>,
    );
    return { ...view, contestId };
  }

  // Collapsed panels are hidden, not unmounted, so these check visibility and
  // pass `hidden: true` to find the element.
  function schemaPanel() {
    return screen.getByRole("region", { name: en.participant.play.schema.heading, hidden: true });
  }
  function sidePanel() {
    return screen.getByRole("tab", { name: en.participant.play.workspace.tabs.story, hidden: true });
  }
  function bottomPanel() {
    return screen.getByText(en.participant.play.workspace.resultEmpty);
  }
  /** The editor by name: the schema panel has a search field too. */
  function editor() {
    return screen.getByRole("textbox", { name: en.participant.console.label });
  }

  test("the schema panel and its divider both leave, and both come back", async () => {
    showWithToggles();
    expect(schemaPanel()).toBeVisible();
    expect(screen.getByRole("separator", { name: panes.schema })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: p.schema }));

    expect(schemaPanel()).not.toBeVisible();
    expect(screen.queryByRole("separator", { name: panes.schema })).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: p.schema }));

    expect(schemaPanel()).toBeVisible();
    expect(screen.getByRole("separator", { name: panes.schema })).toBeInTheDocument();
  });

  test("the side panel and its divider both leave", async () => {
    showWithToggles();

    await userEvent.click(screen.getByRole("button", { name: p.side }));

    expect(sidePanel()).not.toBeVisible();
    expect(screen.queryByRole("separator", { name: panes.side })).not.toBeInTheDocument();
    // The console stays.
    expect(editor()).toBeVisible();
  });

  test("the bottom panel and the edge above it both leave", async () => {
    showWithToggles();

    await userEvent.click(screen.getByRole("button", { name: p.bottom }));

    expect(bottomPanel()).not.toBeVisible();
    expect(screen.queryByRole("separator", { name: panes.editor })).not.toBeInTheDocument();
  });

  test("collapsing all three leaves the tab strip and the editor", async () => {
    showWithToggles();

    await userEvent.click(screen.getByRole("button", { name: p.schema }));
    await userEvent.click(screen.getByRole("button", { name: p.side }));
    await userEvent.click(screen.getByRole("button", { name: p.bottom }));

    expect(schemaPanel()).not.toBeVisible();
    expect(sidePanel()).not.toBeVisible();
    expect(bottomPanel()).not.toBeVisible();
    expect(screen.getByRole("tablist", { name: en.participant.play.workspace.editor.tablist })).toBeVisible();
    expect(editor()).toBeVisible();
  });

  // jsdom lays nothing out, so the template is checked: a collapsed column
  // must not keep a track.
  test("the grid drops the track of a collapsed column", async () => {
    const { container } = showWithToggles();
    const grid = container.querySelector<HTMLElement>('[style*="--pane-schema"]')!;

    expect(grid.style.getPropertyValue("--cols-wide")).toContain("var(--pane-schema)");

    await userEvent.click(screen.getByRole("button", { name: p.schema }));

    expect(grid.style.getPropertyValue("--cols-wide")).not.toContain("var(--pane-schema)");
    expect(grid.style.getPropertyValue("--cols-wide")).toContain("var(--pane-side)");

    await userEvent.click(screen.getByRole("button", { name: p.side }));

    expect(grid.style.getPropertyValue("--cols-wide")).not.toContain("var(--pane-side)");
    expect(grid.style.getPropertyValue("--cols-narrow")).not.toContain("var(--pane-side)");
  });

  // Collapsing does not forget a width the participant set.
  test("a width the participant set survives a collapse and an expand", async () => {
    const { container, contestId } = showWithToggles();
    const grid = container.querySelector<HTMLElement>('[style*="--pane-side"]')!;
    const before = grid.style.getPropertyValue("--pane-side");

    // The arrow keys move the divider too, which jsdom can drive.
    fireEvent.keyDown(screen.getByRole("separator", { name: panes.side }), { key: "ArrowLeft" });
    const widened = grid.style.getPropertyValue("--pane-side");
    expect(widened).not.toBe(before);

    await userEvent.click(screen.getByRole("button", { name: p.side }));
    await userEvent.click(screen.getByRole("button", { name: p.side }));

    expect(grid.style.getPropertyValue("--pane-side")).toBe(widened);
    expect(window.localStorage.getItem(`dbcontest.console.panes.${contestId}`)).toContain(
      String(parseFloat(widened)),
    );
  });

  test("a completed run brings a collapsed bottom panel back", async () => {
    runResult.current = {
      kind: "answer",
      result: { columns: ["id"], rows: [["1"]], truncated: false, rows_affected: 0 },
    };
    showWithToggles(null);
    await userEvent.click(screen.getByRole("button", { name: p.bottom }));
    expect(bottomPanel()).not.toBeVisible();

    await runQuery();

    await waitFor(() => expect(screen.getByRole("table")).toBeInTheDocument());
    expect(screen.getByRole("button", { name: p.bottom })).toHaveAttribute("aria-pressed", "true");
  });

  // Otherwise the toolbar buttons would control a hidden panel.
  test("choosing a bottom tab brings a collapsed bottom panel back", async () => {
    showWithToggles();
    await userEvent.click(screen.getByRole("button", { name: p.bottom }));

    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.tabs.log }));

    expect(screen.getByRole("button", { name: p.bottom })).toHaveAttribute("aria-pressed", "true");
  });

  /**
   * The keys must work mid-query: CodeMirror sees the keydown first, and an
   * unclaimed Ctrl+B in a contenteditable is the browser's "bold".
   */
  test("Ctrl+B with the caret in the editor collapses the schema panel and types nothing", async () => {
    const { container } = showWithToggles();
    await waitForRealEditor(container);
    await userEvent.click(editor());
    await userEvent.keyboard("SELECT 1");

    await userEvent.keyboard("{Control>}b{/Control}");

    expect(schemaPanel()).not.toBeVisible();
    expect(screen.getByRole("button", { name: p.schema })).toHaveAttribute("aria-pressed", "false");
    expect(editor()).toHaveTextContent("SELECT 1");
  });

  // No schema panel and no toggle: the editor still claims the key, but the
  // flag must not be flipped and remembered for the next visit.
  test("Ctrl+B in the editor does nothing in a contest that hides its schema", async () => {
    const { container, contestId } = showWithToggles(null);
    await waitForRealEditor(container);
    await userEvent.click(editor());
    await userEvent.keyboard("SELECT 1");

    await userEvent.keyboard("{Control>}b{/Control}");

    expect(window.localStorage.getItem(`dbcontest.console.collapsed.${contestId}`)).toBeNull();
    expect(editor()).toHaveTextContent("SELECT 1");
  });

  // The editor claims the key (`preventDefault`: no "bold", and no
  // CodeMirror ⌘B cursor move on a Mac), and the window listener stands aside
  // for a prevented key instead of toggling the panel back.
  test("the editor claims the combination, and nothing above it acts on the same press", async () => {
    const { container } = showWithToggles();
    await waitForRealEditor(container);
    const content = container.querySelector(".cm-content")!;
    // Read on the editor's element: after bubbling, the window listener's
    // own `preventDefault` would mask an editor that bound nothing. This
    // listener runs after CodeMirror's, registered on the same node.
    let claimedByTheEditor: boolean | null = null;
    content.addEventListener("keydown", (event) => {
      claimedByTheEditor = event.defaultPrevented;
    });

    fireEvent.keyDown(content, { key: "j", code: "KeyJ", ctrlKey: true });

    expect(claimedByTheEditor).toBe(true);
    expect(bottomPanel()).not.toBeVisible();
  });

  /**
   * Why a collapsed panel is hidden, not unmounted: an answer field is plain
   * state, nothing saved, and on Windows AltGr reports as Ctrl+Alt, so one
   * stray key could discard a typed answer.
   */
  test("a typed answer survives a collapse and an expand", async () => {
    const q = {
      id: "q1",
      locked: false as const,
      kind: "text" as const,
      points: 10,
      isBonus: false,
      choiceIds: [],
      title: "",
      bodyMd: "Who was in the greenhouse?",
      choices: {},
      attemptsRemaining: 3,
      closed: false,
      canAnswer: true,
      correct: false,
      pointsAwarded: 0,
    };
    showWithToggles(null, [{ question: q, index: 1, body: <span>{q.bodyMd}</span> }]);
    await userEvent.click(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.questions }));
    const field = screen.getByRole("textbox", { name: en.participant.play.questions.answerLabel });
    await userEvent.type(field, "the gardener");

    await userEvent.click(screen.getByRole("button", { name: p.side }));
    await userEvent.click(screen.getByRole("button", { name: p.side }));

    expect(
      screen.getByRole("textbox", { name: en.participant.play.questions.answerLabel }),
    ).toHaveValue("the gardener");
  });

  // Unmounting would reset the log to the server-rendered snapshot and drop
  // every loaded page.
  test("collapsing the bottom panel does not remount the query log", async () => {
    showWithToggles(null);
    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.tabs.log }));
    const empty = screen.getByText(en.participant.play.workspace.log.empty);

    await userEvent.click(screen.getByRole("button", { name: p.bottom }));
    await userEvent.click(screen.getByRole("button", { name: p.bottom }));

    expect(screen.getByText(en.participant.play.workspace.log.empty)).toBe(empty);
  });

  // Expanding into the log counts as arriving; the refresh gate is stubbed
  // out at the top of this file.
  test("expanding back into the log refreshes it", async () => {
    showWithToggles(null);
    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.tabs.log }));
    await waitFor(() => expect(logCalls.count).toBeGreaterThan(0));
    await userEvent.click(screen.getByRole("button", { name: p.bottom }));
    const before = logCalls.count;

    await userEvent.click(screen.getByRole("button", { name: p.bottom }));

    await waitFor(() => expect(logCalls.count).toBeGreaterThan(before));
  });

  test("collapsing one panel does not re-render the panel beside it", async () => {
    showWithToggles();
    const sideRendersBefore = renderCounts.side;

    await userEvent.click(screen.getByRole("button", { name: p.bottom }));

    expect(renderCounts.side).toBe(sideRendersBefore);
  });

  // One tree serves both layouts; every pane still laid out must name its
  // own order (see the pane grid's tests).
  test("the panes left after a collapse still each declare their own order", async () => {
    const { container } = showWithToggles();
    await userEvent.click(screen.getByRole("button", { name: p.schema }));
    const grid = container.querySelector<HTMLElement>('[style*="--pane-side"]')!;

    const placed = [...grid.children].filter(
      (child) => !child.classList.contains("max-narrow:hidden") && !child.hasAttribute("hidden"),
    );
    const orders = placed.map((pane) => {
      for (const prefix of ["max-narrow:", "max-wide:"]) {
        for (const cls of pane.classList) {
          const match = new RegExp(`^${prefix}order-(\\d+)$`).exec(cls);
          if (match) return Number(match[1]);
        }
      }
      return null;
    });

    expect(orders).not.toContain(null);
    expect(new Set(orders).size).toBe(orders.length);
  });
});

// jsdom has no `matchMedia`; a stub that matches stands in for a window below
// the narrow breakpoint. The hiding CSS is not applied here, so visibility is
// read from the `hidden` attribute the same state drives.
describe("one pane at a time on a phone", () => {
  const sw = en.participant.play.workspace.phone;

  const A_QUESTION = {
    id: "q1",
    locked: false as const,
    kind: "text" as const,
    points: 10,
    isBonus: false,
    choiceIds: [],
    title: "",
    bodyMd: "Who was in the greenhouse?",
    choices: {},
    attemptsRemaining: 3,
    closed: false,
    canAnswer: true,
    correct: false,
    pointsAwarded: 0,
  };

  /** A window whose width the test can change, as a rotation does. */
  const width = { phone: true, listeners: new Set<() => void>() };
  function rotate(phone: boolean) {
    width.phone = phone;
    act(() => {
      for (const listener of [...width.listeners]) listener();
    });
  }

  beforeEach(() => {
    width.phone = true;
    width.listeners.clear();
    vi.stubGlobal("matchMedia", (query: string) => ({
      get matches() {
        return width.phone;
      },
      media: query,
      // Only the width's own listeners: the editor asks about `print` too.
      addEventListener: (_: string, listener: () => void) => {
        if (query.includes("width")) width.listeners.add(listener);
      },
      removeEventListener: (_: string, listener: () => void) => width.listeners.delete(listener),
    }));
  });

  afterEach(() => {
    runGate.current = null;
    clarificationsRead.current = null;
    runResult.current = { kind: "idle" };
  });

  const AN_ANSWER = {
    kind: "answer",
    result: { columns: ["id"], rows: [["1"]], truncated: false, rows_affected: 0 },
  } as ConsoleState;

  /** Starts a run that stays in flight until the returned function is called. */
  async function startRun() {
    let settle = () => {};
    runGate.current = new Promise<void>((resolve) => {
      settle = resolve;
    });
    runResult.current = AN_ANSWER;
    showOnAPhone();
    await runQuery();
    return async () => {
      await act(async () => {
        settle();
        await runGate.current;
      });
    };
  }

  let contests = 0;
  function showOnAPhone(schema: typeof A_SCHEMA | null = A_SCHEMA) {
    contests += 1;
    const contestId = `phone-${contests}`;
    const view = render(
      <PanelVisibilityProvider contestId={contestId}>
        <PanelToggles dict={en} />
        <Workspace
          accountId="u1"
          contestId={contestId}
          storyBody={<p>A body in the stacks.</p>}
          storyCover={null}
          printView={null}
          storyUnavailable={null}
          questionEntries={[{ question: A_QUESTION, index: 1, body: <span>{A_QUESTION.bodyMd}</span> }]}
          schema={schema}
          sqlMode="read_only"
          resultDownload
          schemaDiagram
          initialLog={freshInitialLog()}
          workspace={A_WORKSPACE}
          locale="en"
          dict={en}
        />
      </PanelVisibilityProvider>,
    );
    return { ...view, contestId };
  }

  function part(name: string) {
    return screen.getByRole("radio", { name });
  }
  function schemaPanel() {
    return screen.getByRole("region", { name: en.participant.play.schema.heading, hidden: true });
  }
  function sidePanel() {
    return screen.getByRole("tab", { name: en.participant.play.workspace.tabs.story, hidden: true });
  }
  function resultPanel() {
    return screen.getByText(en.participant.play.workspace.resultEmpty);
  }
  function editor() {
    return screen.getByRole("textbox", { name: en.participant.console.label, hidden: true });
  }
  function answerField() {
    return screen.getByRole("textbox", { name: en.participant.play.questions.answerLabel, hidden: true });
  }

  test("opens on the editor, with the other three panes mounted and hidden", () => {
    showOnAPhone();

    expect(part(sw.editor)).toBeChecked();
    expect(editor()).toBeVisible();
    expect(resultPanel()).not.toBeVisible();
    expect(schemaPanel()).not.toBeVisible();
    expect(sidePanel()).not.toBeVisible();
  });

  test("each part shows its pane and hides the one before", async () => {
    showOnAPhone();

    await userEvent.click(part(sw.schema));
    expect(schemaPanel()).toBeVisible();
    expect(editor()).not.toBeVisible();

    await userEvent.click(part(sw.side));
    expect(sidePanel()).toBeVisible();
    expect(schemaPanel()).not.toBeVisible();

    await userEvent.click(part(sw.result));
    expect(resultPanel()).toBeVisible();
    expect(sidePanel()).not.toBeVisible();

    await userEvent.click(part(sw.editor));
    expect(editor()).toBeVisible();
    expect(resultPanel()).not.toBeVisible();
  });

  // Before hydration the server's markup is all a phone has, and afterwards a
  // class is what keeps a pane away should the script's reading of the width
  // ever disagree with the stylesheet's.
  test("every pane but the shown one carries the class that hides it below the breakpoint", async () => {
    const { container } = showOnAPhone();
    const paneOf = (name: string) => container.querySelector(`[data-phone-pane="${name}"]`);

    expect(paneOf("editor")).not.toHaveClass("max-narrow:hidden");
    for (const name of ["result", "schema", "side"]) expect(paneOf(name)).toHaveClass("max-narrow:hidden");

    await userEvent.click(part(sw.side));

    expect(paneOf("side")).not.toHaveClass("max-narrow:hidden");
    for (const name of ["editor", "result", "schema"]) expect(paneOf(name)).toHaveClass("max-narrow:hidden");
  });

  test("a completed run switches to the result", async () => {
    runResult.current = {
      kind: "answer",
      result: { columns: ["id"], rows: [["1"]], truncated: false, rows_affected: 0 },
    };
    showOnAPhone();

    await runQuery();

    await waitFor(() => expect(part(sw.result)).toBeChecked());
    expect(screen.getByRole("table")).toBeVisible();
    expect(editor()).not.toBeVisible();
    runResult.current = { kind: "idle" };
  });

  // A run takes seconds under load. Its result arriving is not something the
  // participant asked for, so it may not take the pane or the focus from
  // whatever they moved on to.
  test("a run that settles while another pane is open leaves the pane and the focus, and marks the result", async () => {
    const settle = await startRun();
    await userEvent.click(part(sw.side));
    await userEvent.click(answerField());
    await userEvent.keyboard("the gar");

    await settle();

    await waitFor(() => expect(part(sw.result)).toHaveAccessibleDescription(sw.resultMarked));
    expect(part(sw.side)).toBeChecked();
    expect(answerField()).toBeVisible();
    expect(answerField()).toHaveFocus();
    await userEvent.keyboard("dener");
    expect(answerField()).toHaveValue("the gardener");

    await userEvent.click(part(sw.result));
    expect(screen.getByRole("table")).toBeVisible();
    expect(part(sw.result)).not.toHaveAccessibleDescription();
  });

  test("a run that settles while the result is already open marks nothing", async () => {
    const settle = await startRun();
    await userEvent.click(part(sw.result));

    await settle();

    await waitFor(() => expect(screen.getByRole("table")).toBeVisible());
    expect(part(sw.result)).toBeChecked();
    expect(part(sw.result)).not.toHaveAccessibleDescription();
  });

  // The Run button is disabled while its query runs and hidden with the
  // editor afterwards; without a hand-off the focus is left on `<body>`.
  test("a run from the editor moves the focus to the result part with the pane", async () => {
    const settle = await startRun();
    dropFocus();

    await settle();

    await waitFor(() => expect(part(sw.result)).toBeChecked());
    expect(part(sw.result)).toHaveFocus();
  });

  // Hidden, the side panel must not count an announcement as read: the dot
  // on the questions part is the only word of it a phone gets.
  test("an announcement that arrives while the questions are hidden raises their marker, though its tab was left open", async () => {
    let deliver: (value: unknown) => void = () => {};
    clarificationsRead.current = new Promise((resolve) => {
      deliver = resolve;
    });
    showOnAPhone();
    await userEvent.click(part(sw.side));
    await userEvent.click(screen.getByRole("tab", { name: en.participant.play.clarifications.tab }));
    await userEvent.click(part(sw.editor));

    await act(async () => {
      deliver({
        kind: "ok",
        feed: {
          items: [{ kind: "announcement", id: "a1", postedAt: "2026-11-02T09:00:00.000000Z", body: "Ten minutes left." }],
          next: null,
          open: 0,
        },
      });
      await clarificationsRead.current;
    });

    await waitFor(() =>
      expect(part(sw.side)).toHaveAccessibleDescription(en.participant.play.workspace.panels.sideMarked),
    );

    await userEvent.click(part(sw.side));
    await waitFor(() => expect(part(sw.side)).not.toHaveAccessibleDescription());
  });

  // A phone turned on its side is often wide enough for the pane grid. Turned
  // back, it shows the pane the participant was working in, not the one
  // chosen before the turn.
  test("coming back to phone width shows the pane that holds the focus", async () => {
    const { container } = showOnAPhone();
    await waitForRealEditor(container);
    await userEvent.click(part(sw.side));
    rotate(false);
    await userEvent.click(editor());
    await userEvent.keyboard("SELECT 1");

    rotate(true);

    expect(part(sw.editor)).toBeChecked();
    expect(editor()).toBeVisible();
    expect(editor()).toHaveTextContent("SELECT 1");
  });

  test("coming back to phone width with the focus nowhere keeps the pane chosen before", async () => {
    showOnAPhone();
    await userEvent.click(part(sw.side));
    rotate(false);
    dropFocus();

    rotate(true);

    expect(part(sw.side)).toBeChecked();
  });

  test("a query typed survives a visit to the schema and back, in the same editor", async () => {
    const { container } = showOnAPhone();
    await waitForRealEditor(container);
    const before = editor();
    await userEvent.click(before);
    await userEvent.keyboard("SELECT * FROM suspects");

    await userEvent.click(part(sw.schema));
    await userEvent.click(part(sw.editor));

    expect(editor()).toBe(before);
    expect(editor()).toHaveTextContent("SELECT * FROM suspects");
    expect(editor()).toBeVisible();
  });

  test("an unsent answer survives a switch", async () => {
    showOnAPhone();
    await userEvent.click(part(sw.side));
    await userEvent.type(answerField(), "the gardener");

    await userEvent.click(part(sw.editor));
    expect(answerField()).not.toBeVisible();
    await userEvent.click(part(sw.side));

    expect(answerField()).toBeVisible();
    expect(answerField()).toHaveValue("the gardener");
  });

  test("the schema keeps its scroll box across a switch", async () => {
    showOnAPhone();
    await userEvent.click(part(sw.schema));
    const box = schemaPanel().querySelector<HTMLElement>(".overflow-y-auto")!;
    box.scrollTop = 120;

    await userEvent.click(part(sw.editor));
    await userEvent.click(part(sw.schema));

    expect(schemaPanel().querySelector(".overflow-y-auto")).toBe(box);
    expect(box.scrollTop).toBe(120);
  });

  /** The result pane's own control: which of the result and the log it shows. */
  function shows(name: string) {
    const control = screen.getByRole("group", { name: en.participant.play.workspace.panels.bottom, hidden: true });
    return within(control).getByRole("button", { name, hidden: true });
  }
  const tabs = en.participant.play.workspace.tabs;

  test("the result part shows the result even after the log was looked at", async () => {
    showOnAPhone();
    await userEvent.click(screen.getByRole("button", { name: tabs.log }));
    expect(part(sw.result)).toBeChecked();
    expect(shows(tabs.log)).toHaveAttribute("aria-pressed", "true");

    await userEvent.click(part(sw.editor));
    await userEvent.click(part(sw.result));

    expect(shows(tabs.result)).toHaveAttribute("aria-pressed", "true");
    expect(shows(tabs.log)).toHaveAttribute("aria-pressed", "false");
  });

  // A radio that is already checked fires no change, and the part says Result.
  test("the result part, tapped while its pane shows the log, brings the result back", async () => {
    showOnAPhone();
    await userEvent.click(screen.getByRole("button", { name: tabs.log }));
    expect(shows(tabs.log)).toHaveAttribute("aria-pressed", "true");

    await userEvent.click(part(sw.result));

    expect(part(sw.result)).toBeChecked();
    expect(shows(tabs.result)).toHaveAttribute("aria-pressed", "true");
    expect(shows(tabs.log)).toHaveAttribute("aria-pressed", "false");
  });

  // The editor's toolbar is hidden with the editor, so the pane that holds
  // the result and the log says which it shows and offers the other.
  test("the result pane switches between the result and the log by itself", async () => {
    showOnAPhone();
    await userEvent.click(part(sw.result));
    expect(shows(tabs.result)).toBeVisible();
    expect(shows(tabs.result)).toHaveAttribute("aria-pressed", "true");

    await userEvent.click(shows(tabs.log));
    expect(shows(tabs.log)).toHaveAttribute("aria-pressed", "true");
    expect(part(sw.result)).toBeChecked();

    await userEvent.click(shows(tabs.result));
    expect(shows(tabs.result)).toHaveAttribute("aria-pressed", "true");
  });

  // The mark says a result waits unseen; looking at the log is not seeing it.
  test("the mark on the result part outlives a look at the log and goes when the result is shown", async () => {
    const settle = await startRun();
    await userEvent.click(part(sw.side));
    await settle();
    await waitFor(() => expect(part(sw.result)).toHaveAccessibleDescription(sw.resultMarked));

    await userEvent.click(part(sw.editor));
    await userEvent.click(screen.getByRole("button", { name: tabs.log }));
    expect(part(sw.result)).toBeChecked();
    expect(part(sw.result)).toHaveAccessibleDescription(sw.resultMarked);

    await userEvent.click(shows(tabs.result));
    expect(part(sw.result)).not.toHaveAccessibleDescription();
    expect(screen.getByRole("table")).toBeVisible();
  });

  // The switch's own parts hold the focus of somebody who works it by
  // keyboard; the focus follows the pane rather than stay on a part that is
  // no longer the checked one.
  test("a run that settles with the focus on the editor's part moves it to the result's", async () => {
    const settle = await startRun();
    part(sw.editor).focus();

    await settle();

    await waitFor(() => expect(part(sw.result)).toBeChecked());
    expect(part(sw.result)).toHaveFocus();
  });

  test("a shortcut pressed with the focus on one part moves it to the part it chooses", () => {
    showOnAPhone();
    part(sw.editor).focus();

    fireEvent.keyDown(window, { key: "b", code: "KeyB", ctrlKey: true });

    expect(part(sw.schema)).toBeChecked();
    expect(part(sw.schema)).toHaveFocus();
  });

  test("a table's name writes its query and brings the editor back", async () => {
    const { container } = showOnAPhone();
    await waitForRealEditor(container);
    await userEvent.click(part(sw.schema));

    await userEvent.click(screen.getByRole("button", tableName("guests")));

    expect(part(sw.editor)).toBeChecked();
    expect(editor()).toBeVisible();
    expect(editor()).toHaveTextContent("SELECT * FROM guests");
  });

  test("the shortcuts show a pane and, pressed again, return to the editor", async () => {
    const { contestId } = showOnAPhone();

    fireEvent.keyDown(window, { key: "j", code: "KeyJ", ctrlKey: true });
    expect(part(sw.result)).toBeChecked();
    fireEvent.keyDown(window, { key: "b", code: "KeyB", ctrlKey: true });
    expect(part(sw.schema)).toBeChecked();
    fireEvent.keyDown(window, { key: "b", code: "KeyB", ctrlKey: true, altKey: true });
    expect(part(sw.side)).toBeChecked();
    fireEvent.keyDown(window, { key: "b", code: "KeyB", ctrlKey: true, altKey: true });
    expect(part(sw.editor)).toBeChecked();

    // Nothing was collapsed: the wide layout's memory is not a phone's to change.
    expect(window.localStorage.getItem(`dbcontest.console.collapsed.${contestId}`)).toBeNull();
  });

  test("Ctrl+J with the caret in the editor shows the result and moves the focus to the switch", async () => {
    const { container } = showOnAPhone();
    await waitForRealEditor(container);
    await userEvent.click(editor());
    await userEvent.keyboard("SELECT 1");

    await userEvent.keyboard("{Control>}j{/Control}");

    expect(part(sw.result)).toBeChecked();
    expect(part(sw.result)).toHaveFocus();
    expect(editor()).toHaveTextContent("SELECT 1");
  });

  test("the schema search shortcut shows the schema and puts the caret in the search", async () => {
    showOnAPhone();

    fireEvent.keyDown(document, { key: "k", ctrlKey: true });

    await waitFor(() => expect(part(sw.schema)).toBeChecked());
    expect(screen.getByRole("textbox", { name: en.participant.play.schema.searchLabel })).toHaveFocus();
  });

  test("a panel collapsed on a wide screen is still reachable", async () => {
    window.localStorage.setItem(
      `dbcontest.console.collapsed.phone-${contests + 1}`,
      JSON.stringify({ schema: true, side: true, bottom: true }),
    );
    showOnAPhone();

    await userEvent.click(part(sw.side));
    expect(sidePanel()).toBeVisible();
    await userEvent.click(part(sw.schema));
    expect(schemaPanel()).toBeVisible();
    await userEvent.click(part(sw.result));
    expect(resultPanel()).toBeVisible();
  });

  test("the header's panel toggles are not drawn below the breakpoint", () => {
    showOnAPhone();

    const toggle = screen.getByRole("button", { name: en.participant.play.workspace.panels.side });
    expect(toggle.parentElement).toHaveClass("max-narrow:hidden");
  });
});

describe("from the narrow breakpoint up", () => {
  test("the switch stays in the tree but is not drawn, and every pane shows", () => {
    render(
      <PanelVisibilityProvider contestId="wide-1">
        <Workspace
          accountId="u1"
          contestId="wide-1"
          storyBody={<p>A body in the stacks.</p>}
          storyCover={null}
          printView={null}
          storyUnavailable={null}
          questionEntries={[]}
          schema={A_SCHEMA}
          sqlMode="read_only"
          resultDownload
          schemaDiagram
          initialLog={freshInitialLog()}
          workspace={A_WORKSPACE}
          locale="en"
          dict={en}
        />
      </PanelVisibilityProvider>,
    );

    expect(screen.getByRole("radiogroup", { name: en.participant.play.workspace.phone.label })).toHaveClass(
      "narrow:hidden",
    );
    // The result pane's own result/log control is a phone's alone.
    expect(
      screen.queryByRole("group", { name: en.participant.play.workspace.panels.bottom, hidden: true }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: en.participant.console.label })).toBeVisible();
    expect(screen.getByText(en.participant.play.workspace.resultEmpty)).toBeVisible();
    expect(screen.getByRole("region", { name: en.participant.play.schema.heading })).toBeVisible();
    expect(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.story })).toBeVisible();
  });

  // The Run button is disabled while its query runs, and a browser drops the
  // focus of a control that stops being focusable.
  test("a run by the button hands the focus back to the button when it settles", async () => {
    let settle = () => {};
    runGate.current = new Promise<void>((resolve) => {
      settle = resolve;
    });
    runResult.current = {
      kind: "answer",
      result: { columns: ["id"], rows: [["1"]], truncated: false, rows_affected: 0 },
    };
    show();
    await runQuery();
    dropFocus();

    await act(async () => {
      settle();
      await runGate.current;
    });

    await waitFor(() => expect(screen.getByRole("table")).toBeInTheDocument());
    expect(screen.getByRole("button", { name: en.participant.console.run })).toHaveFocus();
    runGate.current = null;
    runResult.current = { kind: "idle" };
  });

  test("a run leaves the phone's pane alone and shows the result beside the editor", async () => {
    render(
      <PanelVisibilityProvider contestId="wide-2">
        <Workspace
          accountId="u1"
          contestId="wide-2"
          storyBody={null}
          storyCover={null}
          printView={null}
          storyUnavailable={null}
          questionEntries={[]}
          schema={null}
          sqlMode="read_only"
          resultDownload
          schemaDiagram
          initialLog={freshInitialLog()}
          workspace={A_WORKSPACE}
          locale="en"
          dict={en}
        />
      </PanelVisibilityProvider>,
    );

    await runQuery();

    expect(screen.getByRole("textbox", { name: en.participant.console.label })).toBeVisible();
    expect(screen.getByRole("radio", { name: en.participant.play.workspace.phone.editor, hidden: true })).toBeChecked();
  });
});
