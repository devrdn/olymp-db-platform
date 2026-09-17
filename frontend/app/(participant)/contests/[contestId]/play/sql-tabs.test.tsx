import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi, type Mock } from "vitest";

import { MAX_TABS } from "@/lib/api/workspace";
import en from "@/lib/i18n/dictionaries/en";

import { SqlTabStrip, type SqlTabView } from "./sql-tabs";

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
  overrides: { tabs?: SqlTabView[]; activeId?: string; closed?: boolean } = {},
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
      dict={en}
      {...handlers}
    />,
  );
  return handlers;
}

function tab(name: string) {
  return screen.getByRole("tab", { name });
}

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
  test("drops a dragged tab where it was dropped", () => {
    const { onMove } = show();

    fireEvent.dragStart(tab("Alibis"));
    fireEvent.dragOver(tab("Query 1"));
    fireEvent.drop(tab("Query 1"));

    expect(onMove).toHaveBeenCalledWith("t3", 0);
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
