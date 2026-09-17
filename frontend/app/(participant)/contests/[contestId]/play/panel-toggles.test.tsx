import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

import { PanelToggles, PanelVisibilityProvider, useSchemaPanel } from "./panel-toggles";

const t = en.participant.play.workspace.panels;

/**
 * A contest of its own per test.
 *
 * The remembered state is module-level — one record per contest, the way the
 * pane sizes next to it are — so two tests that shared a contest would share
 * whatever the first of them collapsed. Each test naming its own contest is
 * also closer to the truth: a participant's two olympiads are two screens.
 */
let contests = 0;
function aContest() {
  contests += 1;
  return `c${contests}`;
}

/** The header's toggles on their own, above a workspace that has a schema panel. */
function show(contestId: string, { schema = true }: { schema?: boolean } = {}) {
  return render(
    <PanelVisibilityProvider contestId={contestId}>
      <PanelToggles dict={en} />
      <AWorkspace schema={schema} />
    </PanelVisibilityProvider>,
  );
}

/**
 * Stands in for `Workspace`: it tells the header whether this contest has a
 * schema panel at all, and it marks its panes the way the real one does —
 * `data-panel` is how a shortcut knows the focus it is about to hide.
 */
function AWorkspace({ schema }: { schema: boolean }) {
  useSchemaPanel(schema);
  return (
    <>
      <div data-panel="schema">
        <input aria-label="search the schema" />
      </div>
      <div data-panel="side">
        <input aria-label="your answer" />
      </div>
      <div data-panel="bottom">
        <input aria-label="a cell of the result" />
      </div>
      <input aria-label="your query" />
    </>
  );
}

beforeEach(() => {
  window.localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("the panel toggles", () => {
  test("name each panel and start with every one of them showing", () => {
    show(aContest());

    for (const name of [t.schema, t.side, t.bottom]) {
      expect(screen.getByRole("button", { name })).toHaveAttribute("aria-pressed", "true");
    }
  });

  test("each tooltip names the shortcut that works it", () => {
    show(aContest());

    expect(screen.getByRole("button", { name: t.schema })).toHaveAttribute(
      "title",
      t.shortcut.replace("{name}", t.schema).replace("{keys}", t.keys.schema),
    );
    expect(screen.getByRole("button", { name: t.side })).toHaveAttribute(
      "title",
      t.shortcut.replace("{name}", t.side).replace("{keys}", t.keys.side),
    );
    expect(screen.getByRole("button", { name: t.bottom })).toHaveAttribute(
      "title",
      t.shortcut.replace("{name}", t.bottom).replace("{keys}", t.keys.bottom),
    );
  });

  test("a press collapses the panel it names, and the next one brings it back", async () => {
    show(aContest());
    const schema = screen.getByRole("button", { name: t.schema });

    await userEvent.click(schema);
    expect(schema).toHaveAttribute("aria-pressed", "false");

    await userEvent.click(schema);
    expect(schema).toHaveAttribute("aria-pressed", "true");
  });

  test("a press moves one panel and leaves the other two alone", async () => {
    show(aContest());

    await userEvent.click(screen.getByRole("button", { name: t.bottom }));

    expect(screen.getByRole("button", { name: t.bottom })).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByRole("button", { name: t.schema })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: t.side })).toHaveAttribute("aria-pressed", "true");
  });

  // A contest that closed its catalogues has no schema panel at all
  // (`Workspace` renders none), so a control for it would be a control for
  // nothing.
  test("the schema toggle is absent in a contest that hides its schema", () => {
    show(aContest(), { schema: false });

    expect(screen.queryByRole("button", { name: t.schema })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: t.side })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t.bottom })).toBeInTheDocument();
  });

  /**
   * A press on the toggle is the other way to collapse a panel, and it needs
   * the same hand-off as the shortcut.
   *
   * On macOS, Safari and Firefox do not focus a `<button>` when it is
   * clicked — the platform convention, and a real participant's browser.
   * `fireEvent.click` is that browser: the press happens and the focus does
   * not move, which is exactly the case where a hand-off that lived in the
   * key handler alone left the focus in a field that was about to be
   * hidden, and then on `<body>`.
   */
  test("a press takes the focus out of the panel it hides, in a browser that does not focus buttons", async () => {
    show(aContest());
    await userEvent.click(screen.getByRole("textbox", { name: "your answer" }));

    fireEvent.click(screen.getByRole("button", { name: t.side }));

    expect(screen.getByRole("button", { name: t.side })).toHaveFocus();
  });

  test("a press leaves a caret that is not in the panel where it is", async () => {
    show(aContest());
    const query = screen.getByRole("textbox", { name: "your query" });
    await userEvent.click(query);

    fireEvent.click(screen.getByRole("button", { name: t.schema }));

    expect(query).toHaveFocus();
  });

  test("a press that shows a panel again does not move the focus", async () => {
    show(aContest());
    fireEvent.click(screen.getByRole("button", { name: t.bottom }));
    const query = screen.getByRole("textbox", { name: "your query" });
    await userEvent.click(query);

    fireEvent.click(screen.getByRole("button", { name: t.bottom }));

    expect(query).toHaveFocus();
  });

  // The same header draws the waiting room, where there are no panels yet.
  test("nothing is drawn outside a provider", () => {
    render(<PanelToggles dict={en} />);

    expect(screen.queryByRole("button", { name: t.schema })).not.toBeInTheDocument();
  });
});

/**
 * VS Code's own three, and they have to work from wherever the participant
 * is. This is the page half of that: the editor's own keymap carries the
 * same three keys, which `console.test.tsx` covers.
 */
describe("the shortcuts", () => {
  /** One press of a combination, from the page rather than from a field. */
  function press(key: "b" | "j", { alt = false }: { alt?: boolean } = {}) {
    fireEvent.keyDown(document.body, {
      key,
      code: key === "b" ? "KeyB" : "KeyJ",
      ctrlKey: true,
      altKey: alt,
    });
  }

  test("Ctrl+B collapses the schema panel", () => {
    show(aContest());

    press("b");

    expect(screen.getByRole("button", { name: t.schema })).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByRole("button", { name: t.side })).toHaveAttribute("aria-pressed", "true");
  });

  // A contest that closed its catalogues draws no schema panel and offers no
  // toggle for one, so the key has nothing to collapse. Flipping the stored
  // flag anyway is a press that does nothing visible and changes what the
  // next visit restores; the key is still claimed, because Ctrl+B belongs to
  // this screen (it opens the bookmarks in Firefox) even where it is inert.
  test("Ctrl+B changes nothing in a contest that hides its schema", () => {
    const contestId = aContest();
    show(contestId, { schema: false });

    press("b");

    expect(window.localStorage.getItem(`dbcontest.console.collapsed.${contestId}`)).toBeNull();
    expect(screen.getByRole("button", { name: t.side })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: t.bottom })).toHaveAttribute("aria-pressed", "true");
  });

  test("Ctrl+Alt+B collapses the side panel", () => {
    show(aContest());

    press("b", { alt: true });

    expect(screen.getByRole("button", { name: t.side })).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByRole("button", { name: t.schema })).toHaveAttribute("aria-pressed", "true");
  });

  test("Ctrl+J collapses the bottom panel", () => {
    show(aContest());

    press("j");

    expect(screen.getByRole("button", { name: t.bottom })).toHaveAttribute("aria-pressed", "false");
  });

  // The editor's own keymap runs first and calls `preventDefault`, the way
  // any CodeMirror binding does. Without this the key would be handled twice
  // — once in the editor, once here — and the panel would end up exactly
  // where it started.
  test("a key another handler already took is left alone", () => {
    show(aContest());

    const event = new KeyboardEvent("keydown", {
      key: "b",
      code: "KeyB",
      ctrlKey: true,
      bubbles: true,
      cancelable: true,
    });
    event.preventDefault();
    document.body.dispatchEvent(event);

    expect(screen.getByRole("button", { name: t.schema })).toHaveAttribute("aria-pressed", "true");
  });

  /**
   * Collapsing a panel the participant is standing in has to put them
   * somewhere. Left alone, focus falls to `<body>`: the next Tab starts from
   * the top of the document, and a screen reader is told nothing at all
   * about what just happened. The toggle is both the nearest thing to where
   * they were and the control that undoes it.
   */
  test("collapsing by shortcut takes the focus with it, onto the toggle", async () => {
    show(aContest());
    await userEvent.click(screen.getByRole("textbox", { name: "your answer" }));

    press("b", { alt: true });

    expect(screen.getByRole("button", { name: t.side })).toHaveFocus();
  });

  // The ordinary case: the participant is typing a query, and a shortcut
  // that yanked the caret out of the editor would be worse than no shortcut.
  test("the caret stays where it is when the panel being collapsed is not the one holding it", async () => {
    show(aContest());
    const query = screen.getByRole("textbox", { name: "your query" });
    await userEvent.click(query);

    press("b");

    expect(query).toHaveFocus();
  });

  test("expanding a panel does not move the focus", async () => {
    show(aContest());
    press("j");
    const query = screen.getByRole("textbox", { name: "your query" });
    await userEvent.click(query);

    press("j");

    expect(query).toHaveFocus();
  });

  test("a bare letter types rather than collapsing anything", () => {
    show(aContest());

    fireEvent.keyDown(document.body, { key: "b", code: "KeyB" });

    expect(screen.getByRole("button", { name: t.schema })).toHaveAttribute("aria-pressed", "true");
  });
});

describe("what is remembered", () => {
  test("a collapsed panel is still collapsed on the next visit", async () => {
    const contestId = aContest();
    show(contestId);
    await userEvent.click(screen.getByRole("button", { name: t.side }));
    cleanup();

    show(contestId);

    expect(screen.getByRole("button", { name: t.side })).toHaveAttribute("aria-pressed", "false");
  });

  test("one contest's layout says nothing about another's", async () => {
    const first = aContest();
    show(first);
    await userEvent.click(screen.getByRole("button", { name: t.side }));
    cleanup();

    show(aContest());

    expect(screen.getByRole("button", { name: t.side })).toHaveAttribute("aria-pressed", "true");
  });

  // Task 4's lesson, and the reason the state is held in React and only
  // mirrored into storage: a browser that refuses storage — a private
  // window, a locked-down machine in a computer class — must cost the
  // participant nothing this session.
  test("a storage that throws costs the participant nothing", async () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("storage is full");
    });
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("storage is blocked");
    });
    show(aContest());

    await userEvent.click(screen.getByRole("button", { name: t.bottom }));

    expect(screen.getByRole("button", { name: t.bottom })).toHaveAttribute("aria-pressed", "false");
  });

  /**
   * The realistic refusal is not a browser that has switched storage off; it
   * is `QuotaExceededError` on a machine whose storage is full, where
   * reading still works and still answers with whatever was stored last
   * time. Believing that older record would quietly put the panel back and
   * undo what the participant just did.
   */
  test("a refused write is not undone by an older record storage can still read", async () => {
    const contestId = aContest();
    window.localStorage.setItem(
      `dbcontest.console.collapsed.${contestId}`,
      JSON.stringify({ schema: true, side: false, bottom: false }),
    );
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("the quota is exceeded");
    });
    show(contestId);
    const schema = screen.getByRole("button", { name: t.schema });
    expect(schema).toHaveAttribute("aria-pressed", "false");

    await userEvent.click(schema);
    expect(schema).toHaveAttribute("aria-pressed", "true");

    // Anything else that re-renders the screen reads the store again, which
    // is where the older record used to win.
    await userEvent.click(screen.getByRole("button", { name: t.bottom }));

    expect(screen.getByRole("button", { name: t.schema })).toHaveAttribute("aria-pressed", "true");
  });

  test("a stored value this build cannot read is not a reason to fail", () => {
    const contestId = aContest();
    window.localStorage.setItem(`dbcontest.console.collapsed.${contestId}`, "{not json");

    show(contestId);

    expect(screen.getByRole("button", { name: t.schema })).toHaveAttribute("aria-pressed", "true");
  });
});
