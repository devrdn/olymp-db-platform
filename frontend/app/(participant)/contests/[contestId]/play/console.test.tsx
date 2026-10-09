import { act, Profiler } from "react";
import { hydrateRoot } from "react-dom/client";
import { renderToString } from "react-dom/server";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import type { WorkspaceTab } from "@/lib/api/workspace";
import en from "@/lib/i18n/dictionaries/en";

import { ConsoleEditor } from "./console";
import { activeTabStorageKey } from "./use-sql-tabs";
import type { ConsoleState } from "./actions";
import { pasteTargetOf } from "./use-signals";

/** The tab every run reports in fixtures with only one open. */
const FROM_THE_ONE_TAB = { tabTitle: "Query 1" };

/** One tab, as the server creates on a first visit. */
const ONE_TAB: WorkspaceTab[] = [
  { id: "t1", title: "Query 1", body: "", position: 0, updatedAt: "v0" },
];

const TWO_TABS: WorkspaceTab[] = [
  ...ONE_TAB,
  { id: "t2", title: "Suspects", body: "", position: 1, updatedAt: "v0" },
];

/** Two tabs holding different text, so a mix-up shows. */
const TWO_WRITTEN_TABS: WorkspaceTab[] = [
  { id: "t1", title: "Query 1", body: "SELECT * FROM suspects", position: 0, updatedAt: "v0" },
  { id: "t2", title: "Suspects", body: "SELECT * FROM alibis", position: 1, updatedAt: "v0" },
];

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

// The action is the boundary: these check what reaches onResult; what
// callers do with it is tested where they live.
const answer = vi.hoisted(() => ({ current: { kind: "idle" } as ConsoleState }));
/** What the browser's FormData carried into the last run. */
const submitted = vi.hoisted(() => ({ current: null as FormData | null }));
const runQueryAction = vi.hoisted(() =>
  vi.fn(async (_state: ConsoleState, form: FormData) => {
    submitted.current = form;
    return answer.current;
  }),
);

vi.mock("./actions", () => ({ runQueryAction }));

/** The hidden mirror field FormData reads (console.tsx). */
function mirror(container: HTMLElement): HTMLTextAreaElement {
  const el = container.querySelector('textarea[name="sql"]');
  if (!el) throw new Error("expected the hidden mirror textarea");
  return el as HTMLTextAreaElement;
}

/**
 * Waits for CodeMirror, which arrives through a dynamic `import()` on a later
 * microtask, replacing the fallback field.
 */
async function waitForRealEditor(container: HTMLElement) {
  await waitFor(() => expect(container.querySelector(".cm-editor")).toBeInTheDocument());
}

async function run(onResult: (state: ConsoleState) => void) {
  render(<ConsoleEditor accountId="u1" contestId="c1" dict={en} tabs={ONE_TAB} onResult={onResult} />);
  await userEvent.click(screen.getByRole("textbox"));
  await userEvent.keyboard("SELECT 1");
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

    // Waited for: the action settles on a later microtask than the click, and
    // asserting at once was flaky under a full-suite run.
    await waitFor(() => expect(onResult).toHaveBeenLastCalledWith(answer.current, FROM_THE_ONE_TAB));
  });

  test("reports a refusal the same way it reports an answer", async () => {
    answer.current = { kind: "refused", code: "query_too_often" };
    const onResult = vi.fn();
    await run(onResult);

    await waitFor(() => expect(onResult).toHaveBeenLastCalledWith(answer.current, FROM_THE_ONE_TAB));
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
    render(<ConsoleEditor accountId="u1" contestId="c1" dict={en} tabs={ONE_TAB} onResult={vi.fn()} />);
    await userEvent.click(screen.getByRole("textbox"));
    await userEvent.keyboard("SELECT 1");
    await userEvent.click(screen.getByRole("button", { name: en.participant.console.run }));

    expect(screen.getByRole("button", { name: en.participant.console.running })).toBeDisabled();
    resolve({ kind: "idle" });
  });

  // React 19 resets the form when the action settles, refusals included.
  // CodeMirror's content is not a form control, so the visible text survives;
  // this proves it through a real refusal rather than the argument alone.
  test("a run does not clear what the participant was typing, even on a refusal", async () => {
    answer.current = { kind: "refused", code: "query_syntax_error" };
    const onResult = vi.fn();
    const { container } = render(<ConsoleEditor accountId="u1" contestId="c1" dict={en} tabs={ONE_TAB} onResult={onResult} />);
    await waitForRealEditor(container);
    const editor = screen.getByRole("textbox");

    await userEvent.click(editor);
    await userEvent.keyboard("SELECT * FROM suspects WHERE");
    await userEvent.click(screen.getByRole("button", { name: en.participant.console.run }));

    await waitFor(() => expect(onResult).toHaveBeenLastCalledWith(answer.current, FROM_THE_ONE_TAB));
    expect(editor).toHaveTextContent("SELECT * FROM suspects WHERE");
  });

  // The reset still reaches the hidden mirror, a real form control; left
  // alone, the next run without retyping would send an empty string.
  test("the hidden field FormData reads from is restored after a refusal, so a second run without retyping still sends the query", async () => {
    answer.current = { kind: "refused", code: "query_syntax_error" };
    const onResult = vi.fn();
    const { container } = render(<ConsoleEditor accountId="u1" contestId="c1" dict={en} tabs={ONE_TAB} onResult={onResult} />);
    await waitForRealEditor(container);

    await userEvent.click(screen.getByRole("textbox"));
    await userEvent.keyboard("SELECT * FROM suspects WHERE");
    await userEvent.click(screen.getByRole("button", { name: en.participant.console.run }));

    await waitFor(() => expect(onResult).toHaveBeenLastCalledWith(answer.current, FROM_THE_ONE_TAB));
    expect(mirror(container).value).toBe("SELECT * FROM suspects WHERE");
  });

  // A browser restores form values across a soft reload before React
  // attaches, and hydration reuses that node, so the mirror may hold text
  // before any change is reported. Driven through a real `hydrateRoot`; the
  // restored text must also reach the visible editor.
  test("a browser-restored value on the server-rendered node survives hydration and reaches the visible editor", async () => {
    const html = renderToString(<ConsoleEditor accountId="u1" contestId="c1" dict={en} tabs={ONE_TAB} onResult={vi.fn()} />);
    const container = document.createElement("div");
    container.innerHTML = html;
    document.body.appendChild(container);
    const textarea = container.querySelector('textarea[name="sql"]');
    if (!textarea) throw new Error("expected the hidden mirror in the server-rendered markup");

    // The browser's own restore, before React attaches.
    (textarea as HTMLTextAreaElement).value = "SELECT * FROM suspects";

    let root: ReturnType<typeof hydrateRoot> | undefined;
    act(() => {
      root = hydrateRoot(container, <ConsoleEditor accountId="u1" contestId="c1" dict={en} tabs={ONE_TAB} onResult={vi.fn()} />);
    });

    expect((textarea as HTMLTextAreaElement).value).toBe("SELECT * FROM suspects");
    // CodeMirror has not loaded yet, so the fallback field shows the text,
    // read from the mirror.
    expect(within(container).getByRole("textbox")).toHaveValue("SELECT * FROM suspects");

    // Once CodeMirror loads, it takes over the fallback's text.
    await waitForRealEditor(container);
    expect(within(container).getByRole("textbox")).toHaveTextContent("SELECT * FROM suspects");

    root?.unmount();
    container.remove();
  });

  // The editor keeps no React state per keystroke. `Profiler.onRender` fires
  // only on a commit, so silence proves no re-render. The first keystroke
  // after a pause may commit once (the status turns "Saving…"), so it is typed
  // before the counter is cleared.
  test("typing still triggers no re-render of the editor", async () => {
    const onRender = vi.fn();
    const { container } = render(
      <Profiler id="editor" onRender={onRender}>
        <ConsoleEditor accountId="u1" contestId="c1" dict={en} tabs={ONE_TAB} onResult={vi.fn()} />
      </Profiler>,
    );
    await waitForRealEditor(container);
    const editor = screen.getByRole("textbox");
    await userEvent.click(editor);
    await userEvent.keyboard("S");
    onRender.mockClear(); // drop the mount commit, the fallback→CodeMirror swap, the click and the save starting

    await userEvent.keyboard("ELECT * FROM suspects WHERE motive IS NOT NULL");

    expect(onRender).not.toHaveBeenCalled();
  });

  // The wiring from ConsoleEditor to CodeEditor; `code-editor.test.tsx`
  // covers the mark itself.
  test("a syntax error's position reaches the editor as a mark", async () => {
    answer.current = { kind: "refused", code: "query_parse_error", position: 12 };
    const { container } = render(<ConsoleEditor accountId="u1" contestId="c1" dict={en} tabs={ONE_TAB} onResult={vi.fn()} />);
    await waitForRealEditor(container);

    await userEvent.click(screen.getByRole("textbox"));
    await userEvent.keyboard("SELECT * FRO suspects");
    await userEvent.click(screen.getByRole("button", { name: en.participant.console.run }));

    await waitFor(() => expect(container.querySelector(".cm-error-position")).toBeInTheDocument());
  });
});

/**
 * The shortcut printed on the Run button, bound in the editor's keymap ahead
 * of CodeMirror's defaults, where `Mod-Enter` would insert a blank line.
 */
describe("⌘↵", () => {
  test("runs the query from inside the editor", async () => {
    answer.current = {
      kind: "answer",
      result: { columns: ["id"], rows: [["1"]], truncated: false, rows_affected: 0 },
    };
    const onResult = vi.fn();
    const { container } = render(<ConsoleEditor accountId="u1" contestId="c1" dict={en} tabs={ONE_TAB} onResult={onResult} />);
    await waitForRealEditor(container);

    runQueryAction.mockClear();
    await userEvent.click(screen.getByRole("textbox"));
    await userEvent.keyboard("SELECT 1");
    // Ctrl, not Cmd: CodeMirror resolves `Mod` by platform, and the test
    // environment is not a Mac.
    await userEvent.keyboard("{Control>}{Enter}{/Control}");

    await waitFor(() => expect(runQueryAction).toHaveBeenCalled());
  });
});

describe("the SQL editor's one rule", () => {
  // A second statement is refused, so the rule stays in sight.
  test("says one statement at a time, on screen", () => {
    render(<ConsoleEditor accountId="u1" contestId="c1" dict={en} tabs={ONE_TAB} onResult={vi.fn()} />);

    expect(screen.getByText(en.participant.console.hint)).toBeVisible();
  });
});

/**
 * The wiring between the tab strip (`sql-tabs.test.tsx`), the per-tab
 * documents (`code-editor.test.tsx`) and the form: above all, which text a
 * run sends.
 */
describe("the editor's tabs", () => {
  const te = en.participant.play.workspace.editor;

  function tab(name: string) {
    return screen.getByRole("tab", { name });
  }

  async function typeInto(name: string, text: string) {
    await userEvent.click(tab(name));
    await userEvent.click(screen.getByRole("textbox", { name: en.participant.console.label }));
    await userEvent.keyboard(text);
  }

  test("puts a strip of tabs above the editor, and the editor is the open tab's panel", async () => {
    const { container } = render(
      <ConsoleEditor accountId="u1" contestId="c1" dict={en} tabs={TWO_TABS} onResult={vi.fn()} />,
    );
    await waitForRealEditor(container);

    expect(screen.getByRole("tablist", { name: te.tablist })).toBeInTheDocument();
    const panel = screen.getByRole("tabpanel");
    expect(panel).toContainElement(screen.getByRole("textbox", { name: en.participant.console.label }));
    expect(tab("Query 1").getAttribute("aria-controls")).toBe(panel.id);
  });

  test("runs the text of the tab that is open", async () => {
    answer.current = { kind: "idle" };
    const { container } = render(
      <ConsoleEditor accountId="u1" contestId="c1" dict={en} tabs={TWO_TABS} onResult={vi.fn()} />,
    );
    await waitForRealEditor(container);

    await typeInto("Query 1", "SELECT * FROM suspects");
    await typeInto("Suspects", "SELECT * FROM alibis");
    await userEvent.click(screen.getByRole("button", { name: en.participant.console.run }));

    await waitFor(() => expect(submitted.current?.get("sql")).toBe("SELECT * FROM alibis"));

    await userEvent.click(tab("Query 1"));
    await userEvent.click(screen.getByRole("button", { name: en.participant.console.run }));

    await waitFor(() => expect(submitted.current?.get("sql")).toBe("SELECT * FROM suspects"));
  });

  test("keeps what was typed in a tab that is not the one showing", async () => {
    const { container } = render(
      <ConsoleEditor accountId="u1" contestId="c1" dict={en} tabs={TWO_TABS} onResult={vi.fn()} />,
    );
    await waitForRealEditor(container);

    await typeInto("Query 1", "SELECT * FROM suspects");
    await userEvent.click(tab("Suspects"));
    await userEvent.click(tab("Query 1"));

    expect(screen.getByRole("textbox", { name: en.participant.console.label })).toHaveTextContent(
      "SELECT * FROM suspects",
    );
  });

  // The result outlives the tab switch, so it names its tab.
  test("says which tab a run came from", async () => {
    answer.current = {
      kind: "answer",
      result: { columns: ["id"], rows: [["1"]], truncated: false, rows_affected: 0 },
    };
    const onResult = vi.fn();
    const { container } = render(
      <ConsoleEditor accountId="u1" contestId="c1" dict={en} tabs={TWO_TABS} onResult={onResult} />,
    );
    await waitForRealEditor(container);

    await typeInto("Suspects", "SELECT 1");
    await userEvent.click(screen.getByRole("button", { name: en.participant.console.run }));

    await waitFor(() =>
      expect(onResult).toHaveBeenLastCalledWith(answer.current, { tabTitle: "Suspects" }),
    );
  });

  // A client-side navigation from /my has no hydration: the remembered tab
  // opens on the first render, and the editor and the hidden field must both
  // carry its text, or a run sends, and saves, the first tab's text.
  test("opens the tab that was open last time, text and all", async () => {
    window.localStorage.setItem(activeTabStorageKey("c1"), "t2");
    const { container } = render(
      <ConsoleEditor accountId="u1" contestId="c1" dict={en} tabs={TWO_WRITTEN_TABS} onResult={vi.fn()} />,
    );
    await waitForRealEditor(container);

    expect(tab("Suspects")).toHaveAttribute("aria-selected", "true");
    expect(mirror(container).value).toBe("SELECT * FROM alibis");
    expect(screen.getByRole("textbox", { name: en.participant.console.label })).toHaveTextContent(
      "SELECT * FROM alibis",
    );

    await userEvent.click(screen.getByRole("button", { name: en.participant.console.run }));

    await waitFor(() => expect(submitted.current?.get("sql")).toBe("SELECT * FROM alibis"));
  });

  // The fallback field too, before CodeMirror's chunk arrives.
  test("opens the remembered tab in the fallback field too, before CodeMirror loads", () => {
    window.localStorage.setItem(activeTabStorageKey("c1"), "t2");
    render(<ConsoleEditor accountId="u1" contestId="c1" dict={en} tabs={TWO_WRITTEN_TABS} onResult={vi.fn()} />);

    expect(screen.getByRole("textbox", { name: en.participant.console.label })).toHaveValue(
      "SELECT * FROM alibis",
    );
  });

  // As with the notes: an unreadable workspace stops saving, not editing.
  test("still edits, and says nothing is saved, when the workspace could not be read", async () => {
    const { container } = render(
      <ConsoleEditor accountId="u1" contestId="c1" dict={en} tabs={null} onResult={vi.fn()} />,
    );
    await waitForRealEditor(container);

    expect(screen.getAllByRole("tab")).toHaveLength(1);
    expect(screen.getByTestId("sql-tabs-status")).toHaveTextContent(te.unsaved);

    await userEvent.click(screen.getByRole("textbox", { name: en.participant.console.label }));
    await userEvent.keyboard("SELECT 1");
    await userEvent.click(screen.getByRole("button", { name: en.participant.console.run }));

    await waitFor(() => expect(submitted.current?.get("sql")).toBe("SELECT 1"));
  });
});

test("a paste into the SQL editor is watched as one into the editor", async () => {
  const { container } = render(<ConsoleEditor accountId="u1" contestId="c1" dict={en} tabs={ONE_TAB} onResult={vi.fn()} />);
  // CodeMirror mounts after the first render.
  const content = await waitFor(() => {
    const found = container.querySelector(".cm-content");
    expect(found).not.toBeNull();
    return found;
  });
  expect(pasteTargetOf(content)).toBe("editor");
});
