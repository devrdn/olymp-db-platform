import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi, type Mock } from "vitest";

import { MAX_TABS } from "@/lib/api/workspace";
import en from "@/lib/i18n/dictionaries/en";

import { SqlTabStatus, SqlTabStrip, type SqlTabView } from "./sql-tabs";

const t = en.participant.play.workspace.editor;

const THREE: SqlTabView[] = [
  { id: "t1", title: "Query 1" },
  { id: "t2", title: "Suspects" },
  { id: "t3", title: "Alibis" },
];

type Handlers = {
  onSelect: Mock<(id: string) => void>;
  onCreate: Mock<() => void>;
  onRename: Mock<(id: string, title: string) => void>;
  onClose: Mock<(id: string) => void>;
  onMove: Mock<(id: string, to: number) => void>;
};

function show(
  overrides: {
    tabs?: SqlTabView[];
    activeId?: string;
    closed?: boolean;
    status?: React.ReactNode;
  } = {},
): Handlers {
  const handlers: Handlers = {
    onSelect: vi.fn<(id: string) => void>(),
    onCreate: vi.fn<() => void>(),
    onRename: vi.fn<(id: string, title: string) => void>(),
    onClose: vi.fn<(id: string) => void>(),
    onMove: vi.fn<(id: string, to: number) => void>(),
  };
  render(
    <SqlTabStrip
      tabs={overrides.tabs ?? THREE}
      activeId={overrides.activeId ?? "t1"}
      idPrefix="sqltab-"
      panelId="editor-panel"
      closed={overrides.closed ?? false}
      status={overrides.status}
      dict={en}
      {...handlers}
    />,
  );
  return handlers;
}

function tab(name: string) {
  return screen.getByRole("tab", { name });
}

/**
 * Which half of a tab the pointer is over decides which side of it a
 * dragged tab lands on, and jsdom lays nothing out — every rectangle it
 * reports is empty. So the tab being dropped on is given one.
 */
function measured(name: string): HTMLElement {
  const element = tab(name);
  vi.spyOn(element, "getBoundingClientRect").mockReturnValue({
    left: 100,
    right: 180,
    width: 80,
    top: 0,
    bottom: 30,
    height: 30,
    x: 100,
    y: 0,
    toJSON: () => ({}),
  } as DOMRect);
  return element;
}

const LEFT_HALF = 120;
const RIGHT_HALF = 160;

/**
 * jsdom has no `DragEvent`, and `fireEvent`'s own drag events therefore
 * carry no pointer position — which is the one thing these need. A
 * `MouseEvent` of the same name is what React's own listener sees anyway.
 */
function dragTo(
  element: HTMLElement,
  type: "dragover" | "drop",
  clientX: number,
  dataTransfer?: object,
) {
  const event = new MouseEvent(type, { bubbles: true, cancelable: true, clientX });
  if (dataTransfer) Object.defineProperty(event, "dataTransfer", { value: dataTransfer });
  element.dispatchEvent(event);
}

/**
 * A `DataTransfer` this environment does not have. jsdom implements neither
 * it nor `DragEvent`, so what the strip writes onto the drag session — the
 * one thing a real browser needs and a synthesised event does not — is only
 * observable on a stand-in.
 */
function transfer() {
  return { setData: vi.fn<(format: string, data: string) => void>(), effectAllowed: "", dropEffect: "" };
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("the SQL tab strip", () => {
  test("is a tablist with one tab per document, the active one selected", () => {
    show();

    expect(screen.getByRole("tablist", { name: t.tablist })).toBeInTheDocument();
    expect(screen.getAllByRole("tab")).toHaveLength(3);
    expect(tab("Query 1")).toHaveAttribute("aria-selected", "true");
    expect(tab("Suspects")).toHaveAttribute("aria-selected", "false");
    expect(tab("Query 1")).toHaveAttribute("aria-controls", "editor-panel");
  });

  // The roving tabindex the tablist pattern asks for: one stop in the page's
  // own tab order, and the arrows move within the strip from there.
  test("puts only the active tab in the tab order", () => {
    show({ activeId: "t2" });

    expect(tab("Suspects")).toHaveAttribute("tabindex", "0");
    expect(tab("Query 1")).toHaveAttribute("tabindex", "-1");
  });

  test("selects a tab when it is clicked", async () => {
    const { onSelect } = show();

    await userEvent.click(tab("Alibis"));

    expect(onSelect).toHaveBeenCalledWith("t3");
  });

  test("moves between tabs with the arrows and to the ends with Home and End", async () => {
    const { onSelect } = show({ activeId: "t2" });
    tab("Suspects").focus();

    await userEvent.keyboard("{ArrowRight}");
    expect(onSelect).toHaveBeenLastCalledWith("t3");
    // Focus follows the selection, so the next arrow is pressed on the tab
    // the last one landed on — which is what makes walking the strip work.
    expect(tab("Alibis")).toHaveFocus();

    await userEvent.keyboard("{ArrowLeft}");
    expect(onSelect).toHaveBeenLastCalledWith("t2");

    await userEvent.keyboard("{End}");
    expect(onSelect).toHaveBeenLastCalledWith("t3");

    await userEvent.keyboard("{Home}");
    expect(onSelect).toHaveBeenLastCalledWith("t1");
  });

  test("wraps around at both ends of the strip", async () => {
    const { onSelect } = show({ activeId: "t1" });
    tab("Query 1").focus();

    await userEvent.keyboard("{ArrowLeft}");

    expect(onSelect).toHaveBeenLastCalledWith("t3");
  });

  test("opens a new tab from the + at the end of the strip", async () => {
    const { onCreate } = show();

    await userEvent.click(screen.getByRole("button", { name: t.newTab }));

    expect(onCreate).toHaveBeenCalled();
  });

  // A long sentence — "The contest is over, so this tab is no longer
  // saved…" — must give way to the tabs rather than squeeze them. jsdom
  // lays nothing out, so what is checkable is the rule itself.
  test("lets the status line give way to the tabs", () => {
    show({ status: <SqlTabStatus engine={null} error={null} stored={false} dict={en} /> });

    const line = screen.getByTestId("sql-tabs-status");
    expect(line.className).toContain("min-w-0");
    expect(line.className).toMatch(/max-w-/);
    expect(line.className).not.toContain("shrink-0");
  });

  test("refuses an eleventh tab, and says why", () => {
    const tabs = Array.from({ length: MAX_TABS }, (_, i) => ({ id: `t${i}`, title: `Query ${i + 1}` }));
    show({ tabs, activeId: "t0" });

    expect(screen.getByRole("button", { name: t.newTab })).toBeDisabled();
    expect(screen.getByText(en.errors.workspace_tab_limit)).toBeInTheDocument();
  });

  test("closes a tab from its ✕", async () => {
    const { onClose } = show();

    await userEvent.click(screen.getByRole("button", { name: t.close.replace("{tab}", "Suspects") }));

    expect(onClose).toHaveBeenCalledWith("t2");
  });

  // The server refuses to delete the last tab (`workspace_last_tab`), so the
  // strip does not offer what cannot happen.
  test("offers no ✕ when there is only one tab left", () => {
    show({ tabs: [THREE[0]], activeId: "t1" });

    expect(screen.queryByRole("button", { name: /Close/ })).not.toBeInTheDocument();
  });

  test("closes the focused tab with Delete", async () => {
    const { onClose } = show({ activeId: "t2" });
    tab("Suspects").focus();

    await userEvent.keyboard("{Delete}");

    expect(onClose).toHaveBeenCalledWith("t2");
  });
});

describe("renaming a tab", () => {
  function field(title: string) {
    return screen.getByRole("textbox", { name: t.rename.replace("{tab}", title) });
  }

  test("opens the name for editing on a double click, and Enter saves it", async () => {
    const { onRename } = show();

    await userEvent.dblClick(tab("Suspects"));
    await userEvent.clear(field("Suspects"));
    await userEvent.type(field("Suspects"), "Witnesses{Enter}");

    expect(onRename).toHaveBeenCalledWith("t2", "Witnesses");
  });

  test("opens the name for editing on F2", async () => {
    const { onRename } = show({ activeId: "t2" });
    tab("Suspects").focus();

    await userEvent.keyboard("{F2}");
    await userEvent.clear(field("Suspects"));
    await userEvent.type(field("Suspects"), "Witnesses{Enter}");

    expect(onRename).toHaveBeenCalledWith("t2", "Witnesses");
  });

  test("Esc leaves the name as it was", async () => {
    const { onRename } = show();

    await userEvent.dblClick(tab("Suspects"));
    await userEvent.type(field("Suspects"), "!{Escape}");

    expect(onRename).not.toHaveBeenCalled();
    expect(tab("Suspects")).toBeInTheDocument();
  });

  test("says nothing to the server when the name did not change", async () => {
    const { onRename } = show();

    await userEvent.dblClick(tab("Suspects"));
    await userEvent.type(field("Suspects"), "{Enter}");

    expect(onRename).not.toHaveBeenCalled();
  });
});

describe("reordering the tabs", () => {
  // The one part of a drag a synthesised event does not exercise, and the
  // part a real browser refuses to start without: Firefox cancels a drag
  // whose `DataTransfer` was never written to, and Safari is unreliable
  // about it. Every other test here passes with an empty transfer, so
  // nothing but this one would notice that "drag to reorder" (§5) never
  // starts outside Chrome.
  test("hands the drag session the tab it is carrying, as a move", () => {
    show();
    const dataTransfer = transfer();

    fireEvent.dragStart(tab("Alibis"), { dataTransfer });
    expect(dataTransfer.setData).toHaveBeenCalledWith("text/plain", "t3");
    expect(dataTransfer.effectAllowed).toBe("move");

    dragTo(measured("Query 1"), "dragover", LEFT_HALF, dataTransfer);
    expect(dataTransfer.dropEffect).toBe("move");
  });

  test("drops a dragged tab on the side of the target the pointer is on", () => {
    const { onMove } = show();
    const target = measured("Query 1");

    fireEvent.dragStart(tab("Alibis"));
    dragTo(target, "dragover", LEFT_HALF);
    dragTo(target, "drop", LEFT_HALF);

    expect(onMove).toHaveBeenCalledWith("t3", 0);
  });

  // Dragging rightwards is where an index taken straight from the target is
  // wrong: the dragged tab has left its own place by the time it is put
  // back, so "the third tab" is not the third position any more.
  test("drops a tab dragged rightwards where the pointer says, on either side of the target", () => {
    const first = show();
    let target = measured("Alibis");

    fireEvent.dragStart(tab("Query 1"));
    dragTo(target, "dragover", RIGHT_HALF);
    dragTo(target, "drop", RIGHT_HALF);
    // Past the middle of the last tab: Query 1 goes after it, ending the
    // strip — Suspects, Alibis, Query 1.
    expect(first.onMove).toHaveBeenCalledWith("t1", 2);

    cleanup();
    const second = show();
    target = measured("Alibis");

    fireEvent.dragStart(tab("Query 1"));
    dragTo(target, "dragover", LEFT_HALF);
    dragTo(target, "drop", LEFT_HALF);
    // Before the middle: Query 1 takes Alibis' place and Alibis moves on —
    // Suspects, Query 1, Alibis.
    expect(second.onMove).toHaveBeenCalledWith("t1", 1);
  });

  test("shows which side of a tab the drop will land on", () => {
    show();
    const target = measured("Alibis");

    fireEvent.dragStart(tab("Query 1"));
    act(() => dragTo(target, "dragover", LEFT_HALF));
    expect(target).toHaveAttribute("data-drop", "before");

    act(() => dragTo(target, "dragover", RIGHT_HALF));
    expect(target).toHaveAttribute("data-drop", "after");

    fireEvent.dragEnd(tab("Query 1"));
    expect(target).not.toHaveAttribute("data-drop");
  });

  // The same reordering without a pointer: a strip that can only be arranged
  // by dragging cannot be arranged by half the room.
  test("moves the focused tab with Ctrl+Shift+Arrow", async () => {
    const { onMove } = show({ activeId: "t2" });
    tab("Suspects").focus();

    await userEvent.keyboard("{Control>}{Shift>}{ArrowRight}{/Shift}{/Control}");
    expect(onMove).toHaveBeenLastCalledWith("t2", 2);

    await userEvent.keyboard("{Control>}{Shift>}{ArrowLeft}{/Shift}{/Control}");
    expect(onMove).toHaveBeenLastCalledWith("t2", 0);
  });
});

/**
 * Once the contest is over for this participant every write is refused
 * (`contest_not_running` / `contest_finished`), so the strip stops offering
 * writes: what is on screen is still readable, and nothing invites an action
 * that can only fail.
 */
describe("a contest that has ended", () => {
  test("offers no way to create, close, rename or move a tab", async () => {
    const { onRename, onMove } = show({ closed: true });

    expect(screen.queryByRole("button", { name: t.newTab })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Close/ })).not.toBeInTheDocument();

    await userEvent.dblClick(tab("Suspects"));
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();

    tab("Query 1").focus();
    await userEvent.keyboard("{Control>}{Shift>}{ArrowRight}{/Shift}{/Control}");
    expect(onMove).not.toHaveBeenCalled();
    expect(onRename).not.toHaveBeenCalled();
    expect(tab("Suspects")).toHaveAttribute("draggable", "false");
  });

  test("still switches between the tabs", async () => {
    const { onSelect } = show({ closed: true });

    await userEvent.click(tab("Alibis"));

    expect(onSelect).toHaveBeenCalledWith("t3");
  });
});
