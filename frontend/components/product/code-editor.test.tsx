import { act, Profiler, useRef, useState } from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";

import type { GameSchema } from "@/lib/api/schema";

import { CodeEditor, type CodeEditorHandle } from "./code-editor";
import { offeredCompletions, waitForAcceptableCompletion, waitForCompletion } from "./completion-test-support";

/**
 * CodeMirror arrives through a dynamic `import()` that resolves on a later
 * microtask even when cached. `.cm-editor` appears only once `mountEditor` has
 * run, and React removes the fallback field in a later commit, so until then
 * there are two textboxes.
 */
async function waitForRealEditor(container: HTMLElement) {
  await waitFor(() => {
    expect(container.querySelector(".cm-editor")).toBeInTheDocument();
    expect(container.querySelector("textarea")).not.toBeInTheDocument();
  });
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
    await waitForRealEditor(container);

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
    await waitForRealEditor(container);

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
 * Text handed to the editor from outside (a click on a table's name): an edit
 * like typing, so it is undoable and saved, placed where the caret is.
 */
describe("inserting text at the caret", () => {
  const STATEMENT = "SELECT * FROM guests LIMIT 5;";

  function Single({
    initial,
    handle,
    onChange = () => {},
  }: {
    initial: string;
    handle: React.Ref<CodeEditorHandle>;
    onChange?: (text: string) => void;
  }) {
    return (
      <CodeEditor ref={handle} ariaLabel="Your query" placeholder="" getInitialValue={() => initial} onChange={onChange} />
    );
  }

  /** The document's lines, read from CodeMirror's own line elements. */
  function lines(container: HTMLElement) {
    return [...container.querySelectorAll(".cm-line")].map((line) => line.textContent);
  }

  test("goes where the caret is on an empty line, and leaves the caret after it", async () => {
    const handle = { current: null as CodeEditorHandle | null };
    const { container } = render(<Single initial="" handle={handle} />);
    await waitForRealEditor(container);

    act(() => handle.current?.insertAtCursor(STATEMENT));
    act(() => handle.current?.insertAtCursor("SELECT * FROM rooms LIMIT 5;"));

    // The second went after the first, on a line of its own.
    expect(lines(container)).toEqual([STATEMENT, "SELECT * FROM rooms LIMIT 5;"]);
  });

  test("goes on a new line after the caret's line when that line holds text", async () => {
    const handle = { current: null as CodeEditorHandle | null };
    const { container } = render(<Single initial={"SELECT 1\nSELECT 2"} handle={handle} />);
    await waitForRealEditor(container);
    // The participant puts the caret on the first line.
    await userEvent.click(screen.getByRole("textbox"));
    await userEvent.keyboard("{Control>}{Home}{/Control}");

    act(() => handle.current?.insertAtCursor(STATEMENT));

    expect(lines(container)).toEqual(["SELECT 1", STATEMENT, "SELECT 2"]);
  });

  // A caret nobody placed sits at the start, inside whatever statement the
  // document opens with; writing there would split it.
  test("goes at the end, on a line of its own, in a document the participant has not touched", async () => {
    const handle = { current: null as CodeEditorHandle | null };
    const { container } = render(<Single initial={"SELECT *\nFROM x\nWHERE y"} handle={handle} />);
    await waitForRealEditor(container);

    act(() => handle.current?.insertAtCursor(STATEMENT));

    expect(lines(container)).toEqual(["SELECT *", "FROM x", "WHERE y", STATEMENT]);
  });

  test("goes at the end of a document switched to but not yet touched", async () => {
    const handle = { current: null as CodeEditorHandle | null };
    const Documents = () => {
      const [id, setId] = useState("a");
      const texts = useRef(new Map([["a", ""], ["b", "SELECT b\nFROM t"]]));
      return (
        <>
          <button type="button" onClick={() => setId("b")}>
            switch
          </button>
          <CodeEditor
            ref={handle}
            ariaLabel="Your query"
            placeholder=""
            documentId={id}
            getDocumentValue={(key) => texts.current.get(key) ?? ""}
            getInitialValue={() => texts.current.get("a") ?? ""}
            onChange={() => {}}
          />
        </>
      );
    };
    const { container } = render(<Documents />);
    await waitForRealEditor(container);
    // Touching the first document says nothing about the second.
    await userEvent.click(screen.getByRole("textbox"));
    await userEvent.keyboard("{Control>}{Home}{/Control}");
    await userEvent.click(screen.getByRole("button", { name: "switch" }));

    act(() => handle.current?.insertAtCursor(STATEMENT));

    expect(lines(container)).toEqual(["SELECT b", "FROM t", STATEMENT]);
  });

  test("is reported through onChange, so the owner saves it", async () => {
    const onChange = vi.fn();
    const handle = { current: null as CodeEditorHandle | null };
    const { container } = render(<Single initial="SELECT 1" handle={handle} onChange={onChange} />);
    await waitForRealEditor(container);

    act(() => handle.current?.insertAtCursor(STATEMENT));

    expect(onChange).toHaveBeenLastCalledWith(`SELECT 1\n${STATEMENT}`);
  });

  test("is undone in one step, like a paste", async () => {
    const handle = { current: null as CodeEditorHandle | null };
    const { container } = render(<Single initial="SELECT 1" handle={handle} />);
    await waitForRealEditor(container);

    act(() => handle.current?.insertAtCursor(STATEMENT));
    // The insertion leaves the caret in the editor.
    expect(screen.getByRole("textbox")).toHaveFocus();
    await userEvent.keyboard("{Control>}z{/Control}");

    expect(lines(container)).toEqual(["SELECT 1"]);
  });

  test("goes into the document showing, and no other", async () => {
    const handle = { current: null as CodeEditorHandle | null };
    const Documents = () => {
      const [id, setId] = useState("a");
      const texts = useRef(new Map([["a", "SELECT a"], ["b", ""]]));
      return (
        <>
          <button type="button" onClick={() => setId((current) => (current === "a" ? "b" : "a"))}>
            switch
          </button>
          <CodeEditor
            ref={handle}
            ariaLabel="Your query"
            placeholder=""
            documentId={id}
            getDocumentValue={(key) => texts.current.get(key) ?? ""}
            getInitialValue={() => texts.current.get("a") ?? ""}
            onChange={() => {}}
          />
        </>
      );
    };
    const { container } = render(<Documents />);
    await waitForRealEditor(container);

    await userEvent.click(screen.getByRole("button", { name: "switch" }));
    act(() => handle.current?.insertAtCursor(STATEMENT));
    expect(lines(container)).toEqual([STATEMENT]);

    await userEvent.click(screen.getByRole("button", { name: "switch" }));
    expect(lines(container)).toEqual(["SELECT a"]);
  });

  // No `await` before inserting: CodeMirror has not arrived yet.
  test("goes into the fallback field before CodeMirror has loaded, by the same rule", () => {
    const onChange = vi.fn();
    const handle = { current: null as CodeEditorHandle | null };
    render(<Single initial={"SELECT *\nFROM x"} handle={handle} onChange={onChange} />);
    const field = screen.getByRole<HTMLTextAreaElement>("textbox");
    // A caret at the start that nobody placed still means the end.
    field.setSelectionRange(0, 0);

    act(() => handle.current?.insertAtCursor(STATEMENT));

    expect(field).toHaveValue(`SELECT *\nFROM x\n${STATEMENT}`);
    expect(onChange).toHaveBeenLastCalledWith(`SELECT *\nFROM x\n${STATEMENT}`);
  });

  test("goes at the caret the participant placed in the fallback field", () => {
    const handle = { current: null as CodeEditorHandle | null };
    render(<Single initial={"SELECT *\nFROM x"} handle={handle} />);
    const field = screen.getByRole<HTMLTextAreaElement>("textbox");
    act(() => field.focus());
    field.setSelectionRange(0, 0);

    act(() => handle.current?.insertAtCursor(STATEMENT));

    expect(field).toHaveValue(`SELECT *\n${STATEMENT}\nFROM x`);
  });

  // `execCommand("insertText")` is the one way a script's insertion enters a
  // field's native undo stack; jsdom has none, so it is stood in for here.
  test("goes into the fallback field through the browser's own editing, so ⌘Z undoes it", () => {
    const onChange = vi.fn();
    const handle = { current: null as CodeEditorHandle | null };
    render(<Single initial="SELECT 1" handle={handle} onChange={onChange} />);
    const field = screen.getByRole<HTMLTextAreaElement>("textbox");
    const exec = vi.fn((command: string, _ui: boolean, text: string) => {
      if (command !== "insertText") return false;
      field.setRangeText(text, field.selectionStart, field.selectionEnd, "end");
      field.dispatchEvent(new Event("input", { bubbles: true }));
      return true;
    });
    Object.defineProperty(document, "execCommand", { value: exec, configurable: true, writable: true });

    try {
      act(() => handle.current?.insertAtCursor(STATEMENT));
    } finally {
      Reflect.deleteProperty(document, "execCommand");
    }

    expect(exec).toHaveBeenCalledWith("insertText", false, `\n${STATEMENT}`);
    expect(field).toHaveValue(`SELECT 1\n${STATEMENT}`);
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenLastCalledWith(`SELECT 1\n${STATEMENT}`);
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

/**
 * Completion: PostgreSQL's keywords and functions always, the contest's tables
 * and columns when the schema document is given.
 */
describe("completion", () => {
  const SCHEMA: GameSchema = {
    tables: [
      {
        name: "suspects",
        columns: [
          { name: "id", type: "integer", nullable: false, references: "" },
          { name: "motive", type: "text", nullable: true, references: "" },
        ],
      },
      {
        name: "CaseFiles",
        columns: [{ name: "OpenedAt", type: "timestamp", nullable: false, references: "" }],
      },
      { name: "selfies", columns: [] },
      // A column named by a keyword ranks above the keyword itself.
      { name: "events", columns: [{ name: "end", type: "timestamp", nullable: true, references: "" }] },
    ],
    truncated: false,
  };


  async function typeInto(container: HTMLElement, text: string) {
    await waitForRealEditor(container);
    await userEvent.click(screen.getByRole("textbox"));
    await userEvent.keyboard(text);
  }

  function Editor({
    schema,
    onChange = () => {},
    initial = "",
  }: {
    schema?: GameSchema | null;
    onChange?: (text: string) => void;
    initial?: string;
  }) {
    return (
      <CodeEditor
        ariaLabel="Your query"
        placeholder=""
        getInitialValue={() => initial}
        onChange={onChange}
        schema={schema}
      />
    );
  }

  test("offers PostgreSQL's keywords as the participant types", async () => {
    const { container } = render(<Editor />);
    await typeInto(container, "SEL");

    await waitForCompletion("SELECT");
  });

  test("offers PostgreSQL's functions as well as its keywords", async () => {
    const { container } = render(<Editor />);
    await typeInto(container, "SELECT date_tr");

    await waitForCompletion("DATE_TRUNC");
  });

  test("Tab accepts the highlighted option", async () => {
    let text = "";
    const { container } = render(<Editor onChange={(value) => (text = value)} />);
    await typeInto(container, "SELE");
    await waitForAcceptableCompletion("SELECT");

    await userEvent.keyboard("{Tab}");

    expect(text).toBe("SELECT");
    expect(offeredCompletions()).toEqual([]);
  });

  test("Enter accepts the highlighted option rather than breaking the line", async () => {
    let text = "";
    const { container } = render(<Editor onChange={(value) => (text = value)} />);
    await typeInto(container, "SELE");
    await waitForAcceptableCompletion("SELECT");

    await userEvent.keyboard("{Enter}");

    expect(text).toBe("SELECT");
  });

  // Otherwise every line that ends in a table name or a keyword would take two
  // presses of Enter to break.
  test("Enter breaks the line when the option would only repeat the word typed", async () => {
    let text = "";
    const { container } = render(<Editor schema={SCHEMA} onChange={(value) => (text = value)} />);
    await typeInto(container, "SELECT * FROM suspects");
    await waitForAcceptableCompletion("suspects");

    await userEvent.keyboard("{Enter}");

    expect(text).toBe("SELECT * FROM suspects\n");
    expect(offeredCompletions()).toEqual([]);
  });

  test("Enter breaks the line after a keyword typed whole in lower case, leaving its case alone", async () => {
    let text = "";
    const { container } = render(<Editor onChange={(value) => (text = value)} />);
    await typeInto(container, "select");
    await waitForAcceptableCompletion("SELECT");

    await userEvent.keyboard("{Enter}");

    expect(text).toBe("select\n");
  });

  // Typed in either case: in lower case the column matches exactly and ranks
  // first, and its written form is the quoted `"end"`.
  test.each(["END", "end"])(
    "Enter breaks the line after %s, a keyword that is also a column's name, leaving it as typed",
    async (keyword) => {
      let text = "";
      const { container } = render(<Editor schema={SCHEMA} onChange={(value) => (text = value)} />);
      await typeInto(container, `SELECT * FROM events WHERE CASE WHEN true THEN true ${keyword}`);
      await waitForAcceptableCompletion("end");

      await userEvent.keyboard("{Enter}");

      expect(text).toBe(`SELECT * FROM events WHERE CASE WHEN true THEN true ${keyword}\n`);
      expect(offeredCompletions()).toEqual([]);
    },
  );

  // Enter never rewrites a word typed whole; Tab is how to take the quoted form.
  test("Enter breaks the line after a mixed-case table name typed whole, and Tab would quote it", async () => {
    let text = "";
    const { container } = render(<Editor schema={SCHEMA} onChange={(value) => (text = value)} />);
    await typeInto(container, "SELECT * FROM CaseFiles");
    await waitForAcceptableCompletion("CaseFiles");

    await userEvent.keyboard("{Enter}");

    expect(text).toBe("SELECT * FROM CaseFiles\n");
  });

  test("Tab writes a mixed-case table name typed whole in its quoted form", async () => {
    let text = "";
    const { container } = render(<Editor schema={SCHEMA} onChange={(value) => (text = value)} />);
    await typeInto(container, "SELECT * FROM CaseFiles");
    await waitForAcceptableCompletion("CaseFiles");

    await userEvent.keyboard("{Tab}");

    expect(text).toBe('SELECT * FROM "CaseFiles"');
  });

  test("Escape closes the popup and leaves the text as typed", async () => {
    let text = "";
    const { container } = render(<Editor onChange={(value) => (text = value)} />);
    await typeInto(container, "SELE");
    await waitForCompletion("SELECT");

    await userEvent.keyboard("{Escape}");

    expect(offeredCompletions()).toEqual([]);
    expect(text).toBe("SELE");
  });

  test("Tab still indents when no popup is open", async () => {
    let text = "";
    const { container } = render(<Editor initial="SELECT" onChange={(value) => (text = value)} />);
    await waitForRealEditor(container);
    await userEvent.click(screen.getByRole("textbox"));

    await userEvent.keyboard("{Tab}");

    expect(text.length).toBeGreaterThan("SELECT".length);
    expect(text.trim()).toBe("SELECT");
  });

  test("offers the schema's tables", async () => {
    const { container } = render(<Editor schema={SCHEMA} />);
    await typeInto(container, "SELECT * FROM sus");

    await waitForCompletion("suspects");
  });

  test("writes a mixed-case table name quoted, so PostgreSQL does not fold it", async () => {
    let text = "";
    const { container } = render(<Editor schema={SCHEMA} onChange={(value) => (text = value)} />);
    await typeInto(container, "SELECT * FROM CaseF");
    await waitForAcceptableCompletion("CaseFiles");

    await userEvent.keyboard("{Tab}");

    expect(text).toBe('SELECT * FROM "CaseFiles"');
  });

  test("offers a table's columns after its name and a dot, quoted where they need it", async () => {
    let text = "";
    const { container } = render(<Editor schema={SCHEMA} onChange={(value) => (text = value)} />);
    await typeInto(container, 'SELECT "CaseFiles".Op');
    await waitForAcceptableCompletion("OpenedAt");

    await userEvent.keyboard("{Tab}");

    expect(text).toBe('SELECT "CaseFiles"."OpenedAt"');
  });

  test("offers the columns of the tables the statement reads, without a table name in front", async () => {
    const { container } = render(<Editor schema={SCHEMA} />);
    await typeInto(container, "SELECT * FROM suspects WHERE mot");

    await waitForCompletion("motive");
  });

  test("does not offer the columns of a table the statement does not read", async () => {
    const { container } = render(<Editor schema={SCHEMA} />);
    await typeInto(container, 'SELECT * FROM "CaseFiles" WHERE o');
    await waitForCompletion("OpenedAt");

    expect(offeredCompletions()).not.toContain("motive");
  });

  test("offers no table or column when the contest hides its schema", async () => {
    const { container } = render(<Editor schema={null} />);
    await typeInto(container, "SEL");
    await waitForCompletion("SELECT");

    expect(offeredCompletions()).not.toContain("selfies");
  });

  // The view holds each SQL tab as its own document; the schema is the contest's, not a tab's.
  test("offers a schema that arrived while another document was showing", async () => {
    function Tabs({ schema }: { schema: GameSchema | null }) {
      const [id, setId] = useState("a");
      return (
        <>
          <button type="button" onClick={() => setId("b")}>
            second
          </button>
          <CodeEditor
            ariaLabel="Your query"
            placeholder=""
            documentId={id}
            getDocumentValue={() => ""}
            getInitialValue={() => ""}
            onChange={() => {}}
            schema={schema}
          />
        </>
      );
    }
    const { container, rerender } = render(<Tabs schema={null} />);
    await waitForRealEditor(container);

    rerender(<Tabs schema={SCHEMA} />);
    await userEvent.click(screen.getByRole("button", { name: "second" }));
    await typeInto(container, "SELECT * FROM sus");

    await waitForCompletion("suspects");
  });

  test("offers the tables of a schema that arrives after the editor was built, keeping the text and its undo", async () => {
    let text = "";
    const onChange = (value: string) => (text = value);
    const { container, rerender } = render(<Editor schema={null} onChange={onChange} />);
    await typeInto(container, "SELECT 1; ");

    rerender(<Editor schema={SCHEMA} onChange={onChange} />);
    await userEvent.keyboard("sel");
    await waitForCompletion("selfies");
    await userEvent.keyboard("{Escape}");

    expect(text).toBe("SELECT 1; sel");
    await userEvent.keyboard("{Control>}z{/Control}");
    expect(text).not.toBe("SELECT 1; sel");
  });
});
