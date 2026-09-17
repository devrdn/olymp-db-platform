import { act, Profiler, useRef, useState } from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";

import { CodeEditor, type CodeEditorHandle } from "./code-editor";

/**
 * CodeMirror itself arrives through a dynamic `import()` (see code-editor.tsx
 * and code-editor-core.ts's own doc comments for why), which resolves on a
 * later microtask even when the module is already cached — so every test
 * that means to exercise the *real* editor has to wait for it, the same way
 * a participant's browser does. `.cm-editor` is CodeMirror's own root class,
 * present only once `mountEditor` has actually run.
 */
async function waitForRealEditor(container: HTMLElement) {
  await waitFor(() => expect(container.querySelector(".cm-editor")).toBeInTheDocument());
}

describe("the SQL editor", () => {
  // The property this whole redesign exists for: a participant who starts
  // typing the instant the page paints must not lose those keystrokes to a
  // chunk that has not arrived yet. No `await` before the assertion — this
  // has to still be true before the dynamic import has had any chance to
  // resolve, which is exactly the window a naive "render nothing until
  // CodeMirror is ready" version of this component would have failed.
  test("the fallback field is typable immediately, before CodeMirror has loaded", () => {
    const onChange = vi.fn();
    render(
      <CodeEditor ariaLabel="Your query" placeholder="" getInitialValue={() => ""} onChange={onChange} />,
    );

    const editor = screen.getByRole("textbox");
    fireEvent.input(editor, { target: { value: "SELECT 1" } });

    expect(onChange).toHaveBeenCalledWith("SELECT 1");
  });

  test("hands off to CodeMirror once it loads, keeping whatever the fallback already held", async () => {
    const { container } = render(
      <CodeEditor
        ariaLabel="Your query"
        placeholder=""
        getInitialValue={() => "SELECT * FROM suspects"}
        onChange={vi.fn()}
      />,
    );

    await waitForRealEditor(container);

    expect(screen.getByRole("textbox")).toHaveTextContent("SELECT * FROM suspects");
  });

  test("a keystroke into the fallback survives the handoff to CodeMirror", async () => {
    const { container } = render(
      <CodeEditor ariaLabel="Your query" placeholder="" getInitialValue={() => ""} onChange={vi.fn()} />,
    );
    fireEvent.input(screen.getByRole("textbox"), { target: { value: "SELECT 1" } });

    await waitForRealEditor(container);

    expect(screen.getByRole("textbox")).toHaveTextContent("SELECT 1");
  });

  // A participant typing in the fallback the instant the chunk finishes
  // loading must not have the caret dropped on the floor: the fallback is
  // about to unmount out from under them. Without this, continuing to type
  // right through the handoff would go nowhere until they noticed and
  // clicked back in — exactly the "swallowed keystrokes" failure mode this
  // whole fallback design exists to avoid, just moved a few seconds later.
  test("keeps focus on the editor across the handoff, when the fallback had it", async () => {
    const { container } = render(
      <CodeEditor ariaLabel="Your query" placeholder="" getInitialValue={() => ""} onChange={vi.fn()} />,
    );
    const fallback = screen.getByRole("textbox");
    fallback.focus();
    expect(fallback).toHaveFocus();

    await waitForRealEditor(container);

    expect(screen.getByRole("textbox")).toHaveFocus();
  });

  test("does not steal focus across the handoff when the fallback never had it", async () => {
    const { container } = render(
      <CodeEditor ariaLabel="Your query" placeholder="" getInitialValue={() => ""} onChange={vi.fn()} />,
    );
    expect(screen.getByRole("textbox")).not.toHaveFocus();

    await waitForRealEditor(container);

    expect(screen.getByRole("textbox")).not.toHaveFocus();
  });

  test("reports every change through onChange, with the document's current text", async () => {
    const onChange = vi.fn();
    const { container } = render(
      <CodeEditor ariaLabel="Your query" placeholder="" getInitialValue={() => ""} onChange={onChange} />,
    );
    await waitForRealEditor(container);

    await userEvent.click(screen.getByRole("textbox"));
    await userEvent.keyboard("SELECT 1");

    expect(onChange).toHaveBeenLastCalledWith("SELECT 1");
  });

  // The property `ConsoleEditor` depends on: CodeMirror owns its own DOM and
  // never asks React to re-render on a keystroke, the same guarantee the
  // uncontrolled `<textarea>` this replaces already had (kept by a different
  // mechanism — the file doc comment explains which). `Profiler.onRender`
  // fires on an actual commit, so no call while typing is direct proof no
  // re-render happened — not just that the DOM node survived, which
  // reconciliation would preserve either way. This is the test that would
  // fail if a future change routed CodeMirror's text through React state.
  //
  // Waits for the real editor first and on purpose: the fallback-to-CodeMirror
  // handoff is itself one legitimate render (`ready` flipping), and this test
  // is about typing, not about that transition — `workspace.test.tsx` and
  // this file's own "hands off" tests already cover the transition.
  test("typing triggers no re-render, once the real editor has loaded", async () => {
    const onRender = vi.fn();
    const { container } = render(
      <Profiler id="editor" onRender={onRender}>
        <CodeEditor ariaLabel="Your query" placeholder="" getInitialValue={() => ""} onChange={vi.fn()} />
      </Profiler>,
    );
    await waitForRealEditor(container);
    onRender.mockClear(); // drop the mount commit and the fallback→CodeMirror swap

    await userEvent.click(screen.getByRole("textbox"));
    onRender.mockClear(); // drop whatever the click itself may have committed
    await userEvent.keyboard("SELECT * FROM suspects WHERE motive IS NOT NULL");

    expect(onRender).not.toHaveBeenCalled();
  });

  test("marks the character at errorPosition", async () => {
    const { container, rerender } = render(
      <CodeEditor
        ariaLabel="Your query"
        placeholder=""
        getInitialValue={() => "SELECT * FRO suspects"}
        onChange={vi.fn()}
        errorPosition={undefined}
      />,
    );
    await waitForRealEditor(container);
    expect(container.querySelector(".cm-error-position")).not.toBeInTheDocument();

    // Position 14 is 1-based into "SELECT * FRO suspects" — the space right
    // after "FRO", which is where a real parser error for this text points.
    rerender(
      <CodeEditor
        ariaLabel="Your query"
        placeholder=""
        getInitialValue={() => "SELECT * FRO suspects"}
        onChange={vi.fn()}
        errorPosition={14}
      />,
    );
    await waitFor(() => expect(container.querySelector(".cm-error-position")).toBeInTheDocument());
  });

  // A position can arrive before CodeMirror has: the refusal that names one
  // only exists once a query has actually been run, but there is no way to
  // guarantee that took longer than the chunk load. The mark has to apply
  // once the editor exists rather than being silently dropped for having
  // arrived "too early".
  test("an errorPosition present before CodeMirror has loaded is still applied once it has", async () => {
    const { container } = render(
      <CodeEditor
        ariaLabel="Your query"
        placeholder=""
        getInitialValue={() => "SELECT * FRO suspects"}
        onChange={vi.fn()}
        errorPosition={14}
      />,
    );

    await waitFor(() => expect(container.querySelector(".cm-error-position")).toBeInTheDocument());
  });

  test("clears the mark on the next edit — a stale position points at text that may no longer be there", async () => {
    function Harness() {
      const [errorPosition, setErrorPosition] = useState<number | undefined>(5);
      return (
        <CodeEditor
          ariaLabel="Your query"
          placeholder=""
          getInitialValue={() => "SELECT"}
          onChange={() => setErrorPosition(5)}
          errorPosition={errorPosition}
        />
      );
    }
    const { container } = render(<Harness />);
    await waitFor(() => expect(container.querySelector(".cm-error-position")).toBeInTheDocument());

    await userEvent.click(screen.getByRole("textbox"));
    await userEvent.keyboard("X");

    expect(container.querySelector(".cm-error-position")).not.toBeInTheDocument();
  });

  test("points at the last character rather than nothing when the position is past the end of the text", async () => {
    const { container } = render(
      <CodeEditor
        ariaLabel="Your query"
        placeholder=""
        getInitialValue={() => "SELECT"}
        onChange={vi.fn()}
        // "unexpected end of input" is reported one past the last character.
        errorPosition={7}
      />,
    );

    await waitFor(() => expect(container.querySelector(".cm-error-position")).toBeInTheDocument());
  });

  test("carries the aria-label, on the fallback and on the real editor alike", async () => {
    const { container } = render(
      <CodeEditor ariaLabel="Your query" placeholder="" getInitialValue={() => ""} onChange={vi.fn()} />,
    );
    expect(screen.getByRole("textbox")).toHaveAccessibleName("Your query");

    await waitForRealEditor(container);
    expect(screen.getByRole("textbox")).toHaveAccessibleName("Your query");
  });
});

/**
 * The gutter and the Tab key the design's editor has
 * (docs/design/preview.html, "SQL-консоль": the numbers 1..7 run down the
 * left of the query).
 */
describe("the editor's own affordances", () => {
  test("numbers the lines", async () => {
    const { container } = render(
      <CodeEditor
        ariaLabel="query"
        placeholder="SELECT"
        getInitialValue={() => "SELECT 1\nFROM guests\nWHERE id = 2"}
        onChange={() => {}}
      />,
    );
    await waitFor(() => expect(container.querySelector(".cm-editor")).toBeInTheDocument());

    const gutter = container.querySelector(".cm-lineNumbers");
    expect(gutter).not.toBeNull();
    expect(gutter).toHaveTextContent("1");
    expect(gutter).toHaveTextContent("3");
  });

  // Two hours of typing SQL is not a form: Tab indents here, and Escape then
  // Tab is how a keyboard user leaves.
  test("indents with Tab instead of leaving the field", async () => {
    const user = userEvent.setup();
    let text = "";
    const { container } = render(
      <CodeEditor
        ariaLabel="query"
        placeholder="SELECT"
        getInitialValue={() => "SELECT"}
        onChange={(value) => {
          text = value;
        }}
      />,
    );
    await waitFor(() => expect(container.querySelector(".cm-editor")).toBeInTheDocument());

    await user.click(screen.getByRole("textbox"));
    await user.keyboard("{Tab}");

    expect(text).not.toBe("");
    expect(text.length).toBeGreaterThan("SELECT".length);
  });
});

/**
 * One view, several documents — what the participant's SQL tabs are built on
 * (the workspace design, §5). Each document keeps its own `EditorState`, so
 * its undo history and its caret survive being switched away from, and
 * switching is not an edit: nothing is reported through `onChange`.
 */
describe("several documents in one editor", () => {
  function Documents({
    onChange = () => {},
    handle,
    texts = new Map([
      ["a", "SELECT a"],
      ["b", "SELECT b"],
    ]),
  }: {
    onChange?: (id: string, text: string) => void;
    handle?: React.Ref<CodeEditorHandle>;
    texts?: Map<string, string>;
  }) {
    const [id, setId] = useState("a");
    const open = useRef(id);
    open.current = id;
    const held = useRef(texts);
    return (
      <>
        <button type="button" onClick={() => setId(open.current === "a" ? "b" : "a")}>
          switch
        </button>
        <CodeEditor
          ref={handle}
          ariaLabel="Your query"
          placeholder=""
          documentId={id}
          getDocumentValue={(key) => held.current.get(key) ?? ""}
          getInitialValue={() => held.current.get(open.current) ?? ""}
          onChange={(text) => {
            held.current.set(open.current, text);
            onChange(open.current, text);
          }}
        />
      </>
    );
  }

  async function switchDocument() {
    await userEvent.click(screen.getByRole("button", { name: "switch" }));
  }

  test("shows the document it is pointed at, and the other one after a switch", async () => {
    const { container } = render(<Documents />);
    await waitForRealEditor(container);
    expect(screen.getByRole("textbox")).toHaveTextContent("SELECT a");

    await switchDocument();

    expect(screen.getByRole("textbox")).toHaveTextContent("SELECT b");
  });

  test("keeps what was typed in a document while another one was showing", async () => {
    // Read back through `onChange` rather than written out here: where a
    // click lands the caret in this environment is not the point, and
    // spelling the result out would make this a test about that instead.
    let typed = "";
    const { container } = render(<Documents onChange={(id, text) => (typed = id === "a" ? text : typed)} />);
    await waitForRealEditor(container);
    await userEvent.click(screen.getByRole("textbox"));
    await userEvent.keyboard("X");
    expect(typed).not.toBe("SELECT a");

    await switchDocument();
    await switchDocument();

    expect(screen.getByRole("textbox")).toHaveTextContent(typed);
  });

  // The reason each document is a whole `EditorState` rather than a string:
  // undo has to mean "what I did in this tab", not "what I last did
  // anywhere".
  test("keeps each document's own undo history across a switch", async () => {
    const { container } = render(<Documents />);
    await waitForRealEditor(container);
    await userEvent.click(screen.getByRole("textbox"));
    // One character, so the whole edit is one entry in the history however
    // slowly the keystrokes are delivered.
    await userEvent.keyboard("X");

    await switchDocument();
    await switchDocument();
    await userEvent.click(screen.getByRole("textbox"));
    await userEvent.keyboard("{Control>}z{/Control}");

    expect(screen.getByRole("textbox")).toHaveTextContent("SELECT a");
  });

  // Switching is not an edit. If it were reported, every switch would hand
  // one tab's text to the other tab's autosave.
  test("reports nothing through onChange when the document is swapped", async () => {
    const onChange = vi.fn();
    const { container } = render(<Documents onChange={onChange} />);
    await waitForRealEditor(container);
    onChange.mockClear();

    await switchDocument();

    expect(onChange).not.toHaveBeenCalled();
  });

  test("takes a text for a document that is not the one showing", async () => {
    const handle = { current: null as CodeEditorHandle | null };
    const { container } = render(<Documents handle={handle} />);
    await waitForRealEditor(container);
    // Open "b" once, so it is a document the editor is holding rather than
    // one it would read afresh — a draft recovered after a tab has been
    // looked at has to reach the state, not only the owner's own copy.
    await switchDocument();
    await switchDocument();

    handle.current?.setDocumentValue("b", "SELECT recovered");
    await switchDocument();

    expect(screen.getByRole("textbox")).toHaveTextContent("SELECT recovered");
  });

  test("takes a text for the document that is showing", async () => {
    const handle = { current: null as CodeEditorHandle | null };
    const { container } = render(<Documents handle={handle} />);
    await waitForRealEditor(container);

    act(() => handle.current?.setDocumentValue("a", "SELECT recovered"));

    expect(screen.getByRole("textbox")).toHaveTextContent("SELECT recovered");
  });

  // A closed tab's state must not be kept: it is the largest thing a tab
  // owns, and an id the server reused would otherwise open somebody's
  // discarded text.
  test("forgets a document that was dropped", async () => {
    const texts = new Map([
      ["a", "SELECT a"],
      ["b", "SELECT b"],
    ]);
    const handle = { current: null as CodeEditorHandle | null };
    const { container } = render(<Documents handle={handle} texts={texts} />);
    await waitForRealEditor(container);
    await switchDocument();
    await switchDocument();

    handle.current?.dropDocument("b");
    texts.set("b", "SELECT fresh");
    await switchDocument();

    expect(screen.getByRole("textbox")).toHaveTextContent("SELECT fresh");
  });

  // And the ordinary case, which is the one that leaked: the tab being
  // closed is the tab that is open. The showing document's state is not in
  // the map — it is in the view — so the swap to the next tab put it back
  // under the dropped id, where nothing would ever ask for it again and
  // nothing would ever free it.
  test("forgets a document dropped while it was the one showing", async () => {
    const texts = new Map([
      ["a", "SELECT a"],
      ["b", "SELECT b"],
    ]);
    const handle = { current: null as CodeEditorHandle | null };
    const { container } = render(<Documents handle={handle} texts={texts} />);
    await waitForRealEditor(container);

    handle.current?.dropDocument("a");
    await switchDocument();
    texts.set("a", "SELECT fresh");
    await switchDocument();

    expect(screen.getByRole("textbox")).toHaveTextContent("SELECT fresh");
  });
});

/**
 * Keys the surrounding screen owns.
 *
 * The participant's workspace collapses its panels on Ctrl/⌘+B and friends
 * (§8 of the workspace design), and those have to work while the caret is in
 * a query. A listener on the window is not enough: CodeMirror sees a keydown
 * inside its own content first, and what it does with an unclaimed
 * combination is type it or leave it to the browser — Ctrl+B in a
 * contenteditable is "bold". So the owner hands the keys down and they go
 * into the editor's own keymap, which is also what makes the editor call
 * `preventDefault` and the window listener stand aside.
 */
describe("the keys the owner reserves", () => {
  test("runs the owner's handler and leaves the document alone", async () => {
    const collapsed = vi.fn();
    const { container } = render(
      <CodeEditor
        ariaLabel="Your query"
        placeholder=""
        getInitialValue={() => "SELECT 1"}
        onChange={vi.fn()}
        shortcuts={[{ key: "Mod-b", run: collapsed }]}
      />,
    );
    await waitForRealEditor(container);
    await userEvent.click(screen.getByRole("textbox"));

    // Ctrl, not Cmd: CodeMirror resolves `Mod` by platform and the test
    // environment is not a Mac. A participant on a Mac presses ⌘B and
    // reaches the same binding.
    await userEvent.keyboard("{Control>}b{/Control}");

    expect(collapsed).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("textbox")).toHaveTextContent("SELECT 1");
  });

  // The handler is read fresh on every press. The editor is built once and
  // never rebuilt, and the owner's closure is recreated on each of its own
  // renders — a captured one would go stale the first time anything else on
  // the screen moved.
  test("runs the handler the owner has now, not the one it mounted with", async () => {
    const first = vi.fn();
    const second = vi.fn();
    const { container, rerender } = render(
      <CodeEditor
        ariaLabel="Your query"
        placeholder=""
        getInitialValue={() => ""}
        onChange={vi.fn()}
        shortcuts={[{ key: "Mod-b", run: first }]}
      />,
    );
    await waitForRealEditor(container);
    rerender(
      <CodeEditor
        ariaLabel="Your query"
        placeholder=""
        getInitialValue={() => ""}
        onChange={vi.fn()}
        shortcuts={[{ key: "Mod-b", run: second }]}
      />,
    );

    await userEvent.click(screen.getByRole("textbox"));
    await userEvent.keyboard("{Control>}b{/Control}");

    expect(first).not.toHaveBeenCalled();
    expect(second).toHaveBeenCalledTimes(1);
  });

  // A key whose handler has gone is not a key this editor has claimed. The
  // owner stops passing it — the panel it toggled is not on this screen any
  // more, say — and swallowing it would leave the participant with a
  // combination that does nothing at all, rather than whatever CodeMirror or
  // the page would have made of it.
  test("leaves a combination alone once its handler is gone", async () => {
    const { container, rerender } = render(
      <CodeEditor
        ariaLabel="Your query"
        placeholder=""
        getInitialValue={() => ""}
        onChange={vi.fn()}
        shortcuts={[{ key: "Mod-b", run: vi.fn() }]}
      />,
    );
    await waitForRealEditor(container);
    rerender(
      <CodeEditor
        ariaLabel="Your query"
        placeholder=""
        getInitialValue={() => ""}
        onChange={vi.fn()}
        shortcuts={[]}
      />,
    );

    const content = container.querySelector(".cm-content")!;
    const notPrevented = fireEvent.keyDown(content, { key: "b", code: "KeyB", ctrlKey: true });

    expect(notPrevented).toBe(true);
  });

  // What the window listener above this reads to know the key has been dealt
  // with (panel-toggles.tsx): without it the combination would be handled
  // twice and the panel would end up where it started.
  test("marks the key as handled, so nothing above acts on it twice", async () => {
    const { container } = render(
      <CodeEditor
        ariaLabel="Your query"
        placeholder=""
        getInitialValue={() => ""}
        onChange={vi.fn()}
        shortcuts={[{ key: "Mod-b", run: vi.fn() }]}
      />,
    );
    await waitForRealEditor(container);

    const content = container.querySelector(".cm-content")!;
    const notPrevented = fireEvent.keyDown(content, { key: "b", code: "KeyB", ctrlKey: true });

    expect(notPrevented).toBe(false);
  });
});
