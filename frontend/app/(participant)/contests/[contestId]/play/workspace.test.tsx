import { existsSync, readFileSync, statSync } from "node:fs";
import path from "node:path";

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

import { PrintView } from "./print-view";
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

const A_SCHEMA = {
  truncated: false,
  tables: [{ name: "guests", columns: [{ name: "id", type: "uuid", nullable: false, references: "" }] }],
};

// Deliberately different text from `storyBody` below: the print-only copy
// and the on-screen story tab render from two different props
// (`storyMarkdown` vs `storyBody`), and sharing one sentence between them
// would make `getByText` ambiguous the moment both are in the tree at once
// (workspace.tsx's own doc: the print copy stays mounted, only hidden by a
// class jsdom does not apply) — a false green either way a mismatch went.
function show(
  schema: typeof A_SCHEMA | null = null,
  overrides: { storyMarkdown?: string | null; storyUnavailable?: string | null } = {},
) {
  return render(
    <Workspace
      contestId="c1"
      title="The Greenhouse Case"
      storyBody={<p>A body in the stacks.</p>}
      printView={printCopy("storyMarkdown" in overrides ? (overrides.storyMarkdown ?? null) : "The printed case notes.")}
      storyUnavailable={overrides.storyUnavailable ?? null}
      questionEntries={[]}
      schema={schema}
      initialLog={freshInitialLog()}
      locale="en"
      dict={en}
    />,
  );
}

/**
 * What `page.tsx` hands `Workspace` as `printView`: the print copy, already
 * rendered, because `Workspace` is a client component and importing
 * `PrintView` from it shipped `react-markdown` to the browser (that file's own
 * doc, and the client-graph test at the bottom of this one). Built here so
 * every assertion below still reads the real printed output rather than a
 * stand-in.
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

/** The wrapper `workspace.tsx` renders the print-only story into — see that file's own doc for why it is a class, not the `hidden` attribute. */
function printOnlyContainer(container: HTMLElement): HTMLElement | null {
  return (
    [...container.querySelectorAll("div")].find(
      (div) => div.classList.contains("hidden") && div.classList.contains("print:block"),
    ) ?? null
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
    expect(screen.getByRole("button", { name: en.participant.play.workspace.tabs.result })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: en.participant.play.workspace.tabs.log })).toBeInTheDocument();
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

    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.tabs.log }));
    await userEvent.click(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.story }));
    await userEvent.click(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.questions }));
    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.tabs.result }));

    // Not a form control any more (CodeMirror's content div), so the text is
    // read the way any other rendered content is, not through `.value`.
    expect(screen.getByRole("textbox")).toHaveTextContent("SELECT * FROM suspects");
  });

  test("switching a tab does not remount the editor's own DOM node", async () => {
    const { container } = show();
    await waitForRealEditor(container);
    const editor = screen.getByRole("textbox");

    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.tabs.log }));

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
    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.tabs.log }));
    await runQuery();

    await waitFor(() => expect(screen.getByRole("table")).toBeInTheDocument());
    expect(screen.getByRole("button", { name: en.participant.play.workspace.tabs.result })).toHaveAttribute(
      "aria-pressed",
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

    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.tabs.log }));

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

    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.tabs.log }));
    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.tabs.result }));

    expect(renderCounts.result).toBe(resultRendersBefore);
    expect(renderCounts.side).toBe(sideRendersBefore);
  });
});

// Task: printing happens on this screen now, not on a separate route — see
// this file's own doc and print-view.tsx's. jsdom cannot evaluate
// `@media print` (it lays nothing out), so these prove the two things a DOM
// assertion actually can: the print-only copy is in the tree with the right
// content, and the interactive workspace carries the class that removes it
// from a printed page. What a real browser does with those classes is
// checked separately, against the built CSS.
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

  test("the interactive workspace is marked to disappear under print", () => {
    const { container } = show();

    // Workspace renders exactly two top-level siblings: the print-only copy,
    // and the interactive workspace beside it (workspace.tsx's own doc) — so
    // "whichever top-level child is not the print copy" identifies the
    // second without depending on the console markup nested many levels
    // inside it, which also carries `flex min-h-0 flex-col` classes of its
    // own and would make a class-based `closest()` match the wrong ancestor.
    const printOnly = printOnlyContainer(container);
    const workspaceRoot = [...container.children].find((el) => el !== printOnly) as HTMLElement | undefined;
    expect(workspaceRoot).not.toBeUndefined();
    expect(workspaceRoot!.className).toMatch(/(^|\s)print:hidden(\s|$)/);
  });
});

// The design's left column (docs/design/preview.html, "SQL-консоль"). It is
// absent rather than empty in a contest that closed its catalogues: leaving
// the column in place would spend a fifth of the screen saying nothing, and
// the panel would be the very oracle the closed catalogue is hiding.
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
 * jsdom lays nothing out, so nothing here can assert a width. What it can
 * assert is the property the widths came out of: which pane is placed where,
 * and whether each track is allowed to exceed its container. Both defects
 * these cover were invisible in the source and only turned up in a browser
 * (the numbers are in the commit message); what is left behind here is the
 * *rule* each fix established, so the next edit that breaks it is caught
 * where it is cheap.
 */
describe("the pane grid's own shape", () => {
  function paneGrid(container: HTMLElement): HTMLElement {
    const grid = container.querySelector<HTMLElement>('[style*="--pane-schema"]');
    if (!grid) throw new Error("the pane grid was not found");
    return grid;
  }

  /**
   * The `order` a pane declares for one breakpoint range, most specific
   * prefix first — or null where it declares none and would therefore be
   * placed at the CSS default of 0, ahead of every pane that declares one.
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

  /** The panes actually laid out in one range: the ones not hidden there. */
  function placedPanes(grid: HTMLElement, hiddenClass: string): Element[] {
    return [...grid.children].filter((child) => !child.classList.contains(hiddenClass));
  }

  // The defect: the questions' own divider carried no order at all, so grid
  // auto-placement walked it first, put it in the 1fr column and pushed the
  // console into the 1px divider column beside it. Every pane that is laid
  // out in a range has to name its place in that range — a single silent
  // `order: 0` is enough to reorder the whole row.
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

  // The defect: with no `grid-template-columns` at all, this grid's one
  // implicit track is `auto`, and an `auto` track is floored at its
  // content's max-content width — here the result table's own natural
  // width, whatever the last query made it. The editor was sized by the
  // table underneath it and the page scrolled sideways.
  test("the console column declares a track that cannot exceed its container", () => {
    const { container } = show(A_SCHEMA);
    const column = container.querySelector("form")?.closest("div.grid");

    expect(column).not.toBeNull();
    expect(column!.className).toMatch(/(^|\s)grid-cols-1(\s|$)/);
  });

  // The defect: below the narrow breakpoint nothing bounded the result
  // panel's height, so a result laid out at its full natural height inside
  // the page and buried the questions thousands of pixels below the fold.
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

// The one thing about this file that a rendering assertion cannot reach: what
// it drags into the browser.
//
// `Workspace` is a client component, so every module it imports — and every
// module those import, transitively — is compiled into this route's client
// bundle. `PrintView` reaches `StoryText`, which is `react-markdown` and
// `remark-gfm`: a real Markdown parser, measured at 31.9 KiB gzipped in this
// route's own chunks, on the screen a participant spends two hours in. It is
// rendered on the server instead (page.tsx), and nothing about the rendered
// output says so — which is exactly why this is asserted here rather than
// left to whoever next reaches for a component that happens to be convenient.
describe("what Workspace pulls into the client bundle", () => {
  /** Bare package specifiers reachable from `entry`, following this app's own files (relative and `@/`) and stopping at package boundaries. */
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
      // `import type` and `export type` are erased by the compiler and reach
      // no bundle, so following them would fail this for a name only the
      // type-checker ever sees.
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

    // A guard against the guard: if the walk resolved nothing, an empty set
    // would pass the real assertion below for the wrong reason.
    expect(packages.has("react")).toBe(true);

    expect([...packages].filter((name) => name === "react-markdown" || name === "remark-gfm")).toEqual([]);
  });

  // The counterpart: `page.tsx` is a Server Component, and there the parser is
  // exactly where it belongs. Without this, the test above would still pass if
  // somebody deleted the print copy outright instead of moving it.
  test("page.tsx still renders the print copy, on the server, where the parser is free", () => {
    expect(packagesReachableFrom("page.tsx").has("react-markdown")).toBe(true);
    expect(readFileSync(path.resolve(__dirname, "page.tsx"), "utf8")).toContain("<PrintView");
  });
});
