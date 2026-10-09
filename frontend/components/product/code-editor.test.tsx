import { act, Profiler, useRef, useState } from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";

import { CodeEditor, type CodeEditorHandle } from "./code-editor";

/**
 * CodeMirror arrives through a dynamic `import()` that resolves on a later
 * microtask even when cached. `.cm-editor` appears only once `mountEditor` has
 * run.
 */
async function waitForRealEditor(container: HTMLElement) {
  await waitFor(() => expect(container.querySelector(".cm-editor")).toBeInTheDocument());
}

describe("the SQL editor", () => {
  // No `await` before the assertion: this must hold before the dynamic import
  // can resolve.
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

  // `Profiler.onRender` fires only on a commit, so no call while typing proves
  // no re-render. Waits for the real editor first, since the handoff itself is
  // one legitimate render.
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

    // Position 14 (1-based) is the space after "FRO", where a real parser error
    // points.
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

  // The refusal can arrive before the chunk does; the mark must apply once the
  // editor exists.
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

/** The gutter and the Tab key from the design. */
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

  // Tab indents; Escape then Tab leaves.
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
 * One view, several documents (the participant's SQL tabs). Each keeps its own
 * `EditorState`, so undo history and caret survive a switch, and a switch is
 * not reported through `onChange`.
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
    // Read back through `onChange`: where a click lands the caret here is not
    // the point.
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

  test("keeps each document's own undo history across a switch", async () => {
    const { container } = render(<Documents />);
    await waitForRealEditor(container);
    await userEvent.click(screen.getByRole("textbox"));
    // One character, so the edit is one history entry however slowly keys
    // arrive.
    await userEvent.keyboard("X");

    await switchDocument();
    await switchDocument();
    await userEvent.click(screen.getByRole("textbox"));
    await userEvent.keyboard("{Control>}z{/Control}");

    expect(screen.getByRole("textbox")).toHaveTextContent("SELECT a");
  });

  // If reported, a switch would hand one tab's text to the other tab's
  // autosave.
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
    // Open "b" once, so the draft must reach the held state, not only the
    // owner's copy.
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

  // A reused id must not reopen discarded text.
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

  // The showing document lives in the view, not the map, so the next swap must
  // not store it back under the dropped id.
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
 * Keys the surrounding screen owns. CodeMirror sees a keydown first and would
 * type or pass on an unclaimed one, so the owner's keys go into the editor's
 * keymap, which also calls `preventDefault`.
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

    // Ctrl, not Cmd: `Mod` resolves by platform and the test environment is not
    // a Mac.
    await userEvent.keyboard("{Control>}b{/Control}");

    expect(collapsed).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("textbox")).toHaveTextContent("SELECT 1");
  });

  // The editor is built once; a captured closure would go stale.
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

  // Swallowing an unclaimed key would leave a combination that does nothing.
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

  // The window listener (panel-toggles.tsx) reads this; otherwise the panel
  // toggles twice.
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
