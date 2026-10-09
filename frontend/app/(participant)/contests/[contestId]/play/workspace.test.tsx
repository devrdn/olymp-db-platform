import { existsSync, readFileSync, statSync } from "node:fs";
import path from "node:path";

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

import { PanelToggles, PanelVisibilityProvider } from "./panel-toggles";
import { PrintView } from "./print-view";
import type { QuestionEntry } from "./questions-panel";
import { StoryCover } from "./story-cover";
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
    workspace?: typeof A_WORKSPACE | null;
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
      questionEntries={[]}
      schema={schema}
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
  await waitFor(() => expect(container.querySelector(".cm-editor")).toBeInTheDocument());
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

  // The log's reads share the participant's `AdmitRead` budget, and a run
  // switches the bottom panel to "Result" anyway.
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
    expect(screen.getByRole("button", { name: /guests/ })).toBeInTheDocument();
  });

  test("is absent entirely in a contest that hides it", () => {
    show(null);

    expect(
      screen.queryByRole("region", { name: en.participant.play.schema.heading }),
    ).not.toBeInTheDocument();
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

  test("every pane placed below the narrow breakpoint declares its own order, and no two share one", () => {
    const { container } = show(A_SCHEMA);
    const grid = paneGrid(container);

    // Below 47.5rem both handles are hidden and the three panels stack.
    const orders = placedPanes(grid, "max-narrow:hidden")
      .filter((pane) => !pane.classList.contains("max-wide:hidden"))
      .map((pane) => declaredOrder(pane, ["max-narrow:", "max-wide:"]));

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

  // Unbounded, a large result buries the questions far below the fold.
  test("the bottom panel keeps a height bound of its own on the narrow fallback", () => {
    const { container } = show(A_SCHEMA);
    const panes = [...container.querySelectorAll("div")].filter(
      (div) => div.classList.contains("overflow-hidden") && div.classList.contains("flex-1"),
    );

    // The result pane and the log pane, both mounted, side by side.
    expect(panes).toHaveLength(2);
    expect(panes[0].parentElement).toBe(panes[1].parentElement);
    expect(panes[0].parentElement!.className).toMatch(/max-narrow:max-h-\[/);
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
      kind: "text" as const,
      points: 10,
      choiceIds: [],
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
