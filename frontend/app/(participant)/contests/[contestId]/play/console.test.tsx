import { act, Profiler } from "react";
import { hydrateRoot } from "react-dom/client";
import { renderToString } from "react-dom/server";
import { render, screen, waitFor, within } from "@testing-library/react";
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

/** The hidden mirror field FormData actually reads — see console.tsx's doc. */
function mirror(container: HTMLElement): HTMLTextAreaElement {
  const el = container.querySelector('textarea[name="sql"]');
  if (!el) throw new Error("expected the hidden mirror textarea");
  return el as HTMLTextAreaElement;
}

/**
 * CodeMirror arrives through a dynamic `import()` (code-editor.tsx's own doc
 * comment says why), which resolves on a later microtask even when the
 * module is already cached. A test that needs the *real* editor — rather
 * than the always-typable fallback field CodeEditor shows until then — waits
 * for `.cm-editor`, CodeMirror's own root class, the same way a participant's
 * browser does.
 */
async function waitForRealEditor(container: HTMLElement) {
  await waitFor(() => expect(container.querySelector(".cm-editor")).toBeInTheDocument());
}

async function run(onResult: (state: ConsoleState) => void) {
  render(<ConsoleEditor contestId="c1" dict={en} onResult={onResult} />);
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
    await userEvent.click(screen.getByRole("textbox"));
    await userEvent.keyboard("SELECT 1");
    await userEvent.click(screen.getByRole("button", { name: en.participant.console.run }));

    expect(screen.getByRole("button", { name: en.participant.console.running })).toBeDisabled();
    resolve({ kind: "idle" });
  });

  // Finding 2 (original): React 19 calls `requestFormReset` on this form once
  // the action settles, regardless of whether it succeeded, and the native
  // reset algorithm wipes an uncontrolled form field back to its
  // `defaultValue`. That used to be the *visible* textarea; a refused query
  // silently erased exactly what a participant was mid-debugging, for the
  // whole two hours. CodeMirror's content div is not a form-associated
  // element at all, so `requestFormReset` cannot reach it — the visible
  // editor is architecturally immune now, not merely restored fast enough to
  // avoid a flash. This is run through a real refusal end to end anyway (not
  // a mock of the reset itself) so it proves the actual DOM behaviour rather
  // than the architecture argument on its own.
  test("a run does not clear what the participant was typing, even on a refusal", async () => {
    answer.current = { kind: "refused", code: "query_syntax_error" };
    const onResult = vi.fn();
    const { container } = render(<ConsoleEditor contestId="c1" dict={en} onResult={onResult} />);
    await waitForRealEditor(container);
    const editor = screen.getByRole("textbox");

    await userEvent.click(editor);
    await userEvent.keyboard("SELECT * FROM suspects WHERE");
    await userEvent.click(screen.getByRole("button", { name: en.participant.console.run }));

    await waitFor(() => expect(onResult).toHaveBeenLastCalledWith(answer.current));
    expect(editor).toHaveTextContent("SELECT * FROM suspects WHERE");
  });

  // What replaces finding 2's own restore, now aimed at the field nobody
  // sees: `requestFormReset` still resets the *mirror* textarea (it is a
  // real form control), and nothing stops that. Unlike the visible editor,
  // losing sync there has a real, if invisible, consequence — the next "Run"
  // click with nothing retyped would submit an empty string — so the mirror
  // needs the same imperative fix-up finding 2's own textarea used to need.
  test("the hidden field FormData reads from is restored after a refusal, so a second run without retyping still sends the query", async () => {
    answer.current = { kind: "refused", code: "query_syntax_error" };
    const onResult = vi.fn();
    const { container } = render(<ConsoleEditor contestId="c1" dict={en} onResult={onResult} />);
    await waitForRealEditor(container);

    await userEvent.click(screen.getByRole("textbox"));
    await userEvent.keyboard("SELECT * FROM suspects WHERE");
    await userEvent.click(screen.getByRole("button", { name: en.participant.console.run }));

    await waitFor(() => expect(onResult).toHaveBeenLastCalledWith(answer.current));
    expect(mirror(container).value).toBe("SELECT * FROM suspects WHERE");
  });

  // Finding 1 (regression from the finding-2 fix, original story): the
  // restore effect seeded `lastTyped` to `""` and ran un-gated on mount. A
  // browser restores form field values across a soft reload independently of
  // React, and hydration reuses that server-rendered node rather than
  // replacing it — so the mirror can already hold real text the instant this
  // component's effects first run, before any change has been reported to
  // populate `lastTyped`. This drives an actual `hydrateRoot` over
  // server-rendered markup, not a mock, so it proves the real DOM behaviour —
  // and, because CodeEditor reads the mirror's value as its own initial
  // document, it proves the restored text actually reaches what the
  // participant sees, not only the hidden field behind it.
  test("a browser-restored value on the server-rendered node survives hydration and reaches the visible editor", async () => {
    const html = renderToString(<ConsoleEditor contestId="c1" dict={en} onResult={vi.fn()} />);
    const container = document.createElement("div");
    container.innerHTML = html;
    document.body.appendChild(container);
    const textarea = container.querySelector('textarea[name="sql"]');
    if (!textarea) throw new Error("expected the hidden mirror in the server-rendered markup");

    // Simulate the browser's own restore, which happens before React ever
    // attaches — hydration must not treat this as stale content to discard.
    (textarea as HTMLTextAreaElement).value = "SELECT * FROM suspects";

    let root: ReturnType<typeof hydrateRoot> | undefined;
    act(() => {
      root = hydrateRoot(container, <ConsoleEditor contestId="c1" dict={en} onResult={vi.fn()} />);
    });

    expect((textarea as HTMLTextAreaElement).value).toBe("SELECT * FROM suspects");
    // The dynamic import CodeMirror arrives through has not resolved this
    // soon after hydration, so what a participant sees right now is the
    // fallback field — carrying the same restored text, read from the
    // mirror above the instant this component's own effects first ran.
    expect(within(container).getByRole("textbox")).toHaveValue("SELECT * FROM suspects");

    // And the value survives the rest of the journey too: once CodeMirror
    // actually loads, it has to pick up what the fallback was holding, not
    // whatever the mirror's own server-rendered `defaultValue` was.
    await waitForRealEditor(container);
    expect(within(container).getByRole("textbox")).toHaveTextContent("SELECT * FROM suspects");

    root?.unmount();
    container.remove();
  });

  // The property the review specifically asked not to be given up in fixing
  // finding 2: the editor has no `onChange` wired to React state, and the fix
  // must not add one in disguise. `Profiler`'s `onRender` only fires on an
  // actual commit, so no call while typing is direct proof no re-render
  // happened — not just that the DOM node survived, which reconciliation
  // would preserve either way.
  test("typing still triggers no re-render of the editor", async () => {
    const onRender = vi.fn();
    const { container } = render(
      <Profiler id="editor" onRender={onRender}>
        <ConsoleEditor contestId="c1" dict={en} onResult={vi.fn()} />
      </Profiler>,
    );
    await waitForRealEditor(container);
    const editor = screen.getByRole("textbox");
    await userEvent.click(editor);
    onRender.mockClear(); // drop the mount commit, the fallback→CodeMirror swap and the click; only typing matters here

    await userEvent.keyboard("SELECT * FROM suspects WHERE motive IS NOT NULL");

    expect(onRender).not.toHaveBeenCalled();
  });

  // Task 1's own second requirement: a syntax error's position, carried by
  // the refusal, has to actually reach the editor a participant is looking
  // at — this is the wiring between `ConsoleEditor` and `CodeEditor`;
  // `code-editor.test.tsx` covers what the mark does once it gets there.
  test("a syntax error's position reaches the editor as a mark", async () => {
    answer.current = { kind: "refused", code: "query_parse_error", position: 12 };
    const { container } = render(<ConsoleEditor contestId="c1" dict={en} onResult={vi.fn()} />);
    await waitForRealEditor(container);

    await userEvent.click(screen.getByRole("textbox"));
    await userEvent.keyboard("SELECT * FRO suspects");
    await userEvent.click(screen.getByRole("button", { name: en.participant.console.run }));

    await waitFor(() => expect(container.querySelector(".cm-error-position")).toBeInTheDocument());
  });
});
