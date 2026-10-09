import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

import { PanelToggles, PanelVisibilityProvider, useSchemaPanel } from "./panel-toggles";

const t = en.participant.play.workspace.panels;

/** One contest per test: the remembered state is module-level, per contest. */
let contests = 0;
function aContest() {
  contests += 1;
  return `c${contests}`;
}

/** The header's toggles above a stand-in workspace. */
function show(contestId: string, { schema = true }: { schema?: boolean } = {}) {
  return render(
    <PanelVisibilityProvider contestId={contestId}>
      <PanelToggles dict={en} />
      <AWorkspace schema={schema} />
    </PanelVisibilityProvider>,
  );
}

/**
 * Stands in for `Workspace`: reports whether there is a schema panel and
 * marks its panes with `data-panel`, which the focus hand-off reads.
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

  // No schema panel, so no control for one.
  test("the schema toggle is absent in a contest that hides its schema", () => {
    show(aContest(), { schema: false });

    expect(screen.queryByRole("button", { name: t.schema })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: t.side })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t.bottom })).toBeInTheDocument();
  });

  /**
   * A click needs the same focus hand-off as the shortcut: Safari and Firefox
   * on macOS do not focus a clicked button, and `fireEvent.click` behaves the
   * same, so the focus would stay in a field about to be hidden.
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

  // The waiting room draws the same header with no panels.
  test("nothing is drawn outside a provider", () => {
    render(<PanelToggles dict={en} />);

    expect(screen.queryByRole("button", { name: t.schema })).not.toBeInTheDocument();
  });
});

/**
 * The page-level listener; the editor's keymap carries the same keys
 * (`console.test.tsx`).
 */
describe("the shortcuts", () => {
  /** One press of a combination, from the page rather than a field. */
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

  // No schema panel: the flag must not flip and be restored next visit, but
  // the key is still claimed (Ctrl+B opens the bookmarks in Firefox).
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

  // The editor's keymap runs first and calls `preventDefault`; handling the
  // key again here would toggle the panel straight back.
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
   * Without the hand-off, focus falls to `<body>` and a screen reader hears
   * nothing; the toggle is nearest and undoes the collapse.
   */
  test("collapsing by shortcut takes the focus with it, onto the toggle", async () => {
    show(aContest());
    await userEvent.click(screen.getByRole("textbox", { name: "your answer" }));

    press("b", { alt: true });

    expect(screen.getByRole("button", { name: t.side })).toHaveFocus();
  });

  // Collapsing another panel must not pull the caret out of the editor.
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

  // The state lives in React and is only mirrored to storage, so refused
  // storage costs nothing this session.
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
   * The realistic refusal is `QuotaExceededError`: reads still return the
   * older record, which must not put the panel back.
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

    // Any re-render reads the store again.
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
