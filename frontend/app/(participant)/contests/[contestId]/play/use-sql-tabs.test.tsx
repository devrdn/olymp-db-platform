import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi, type Mock } from "vitest";

import type { WorkspaceTab } from "@/lib/api/workspace";
import en from "@/lib/i18n/dictionaries/en";

import { SqlTabStatus, SqlTabStrip } from "./sql-tabs";
import { draftStorageKey } from "./use-autosave";
import { activeTabStorageKey, useSqlTabs } from "./use-sql-tabs";

const t = en.participant.play.workspace.editor;

type Call = { url: string; init: RequestInit };
let calls: Call[] = [];
let answer: (url: string, init: RequestInit) => Response;
let confirmClose: Mock<(title: string) => boolean>;
let restored: [string, string][];
let dropped: string[];

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}

function refusal(status: number, code: string) {
  return json({ error: { code, message: "no" } }, status);
}

/** The server's ordinary answers: a save, a new tab, a deletion, a new order. */
function served(url: string, init: RequestInit): Response {
  if (init.method === "POST") {
    return json({ id: "t9", title: "Query 2", body: "", position: 9, updated_at: "v9" });
  }
  if (init.method === "PATCH") return json({ updated_at: "v1" });
  return new Response(null, { status: 204 });
}

const TABS: WorkspaceTab[] = [
  { id: "t1", title: "Query 1", body: "SELECT 1", position: 0, updatedAt: "v0" },
  { id: "t2", title: "Suspects", body: "SELECT 2", position: 1, updatedAt: "v0" },
];

/**
 * The hook, the strip and a stand-in for the editor: a plain textarea that
 * reports what is typed into the tab that is showing, which is exactly the
 * contract `CodeEditor` keeps (`code-editor.test.tsx` covers the real one).
 */
function Harness({ initial }: { initial: WorkspaceTab[] | null }) {
  const tabs = useSqlTabs({
    contestId: "c1",
    initial,
    localTitle: t.local,
    confirmClose,
    onRestore: (id, text) => restored.push([id, text]),
    onDrop: (id) => dropped.push(id),
  });

  return (
    <div>
      <SqlTabStrip
        tabs={tabs.tabs}
        activeId={tabs.activeId}
        idPrefix="sqltab-"
        panelId="editor"
        closed={tabs.closed !== null}
        dict={en}
        status={
          <SqlTabStatus status={tabs.status} error={tabs.error} stored={tabs.stored} dict={en} />
        }
        onSelect={tabs.select}
        onCreate={tabs.create}
        onRename={tabs.rename}
        onClose={tabs.close}
        onMove={tabs.move}
      />
      <textarea
        id="editor"
        aria-label="editor"
        key={tabs.activeId}
        defaultValue={tabs.textOf(tabs.activeId)}
        onChange={(event) => tabs.edited(tabs.activeId, event.currentTarget.value)}
      />
    </div>
  );
}

function show(initial: WorkspaceTab[] | null = TABS) {
  return render(<Harness initial={initial} />);
}

function editor() {
  return screen.getByRole("textbox", { name: "editor" });
}

function type(text: string) {
  fireEvent.change(editor(), { target: { value: text } });
}

function tab(name: string) {
  return screen.getByRole("tab", { name });
}

async function wait(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

async function settle() {
  await act(async () => {});
}

function status() {
  return screen.getByTestId("sql-tabs-status").textContent;
}

function requests(method: string) {
  return calls.filter((call) => call.init.method === method);
}

beforeEach(() => {
  vi.useFakeTimers();
  window.localStorage.clear();
  calls = [];
  restored = [];
  dropped = [];
  answer = served;
  confirmClose = vi.fn<(title: string) => boolean>(() => true);
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init: RequestInit) => {
      calls.push({ url, init });
      return answer(url, init);
    }),
  );
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("the participant's SQL tabs", () => {
  test("open with what the page read, the first one showing", () => {
    show();

    expect(screen.getAllByRole("tab").map((el) => el.getAttribute("aria-label"))).toEqual([
      "Query 1",
      "Suspects",
    ]);
    expect(editor()).toHaveValue("SELECT 1");
    expect(status()).toBe(t.status.saved);
  });

  test("show the tab that was open last time, per contest", () => {
    window.localStorage.setItem(activeTabStorageKey("c1"), "t2");

    show();

    expect(tab("Suspects")).toHaveAttribute("aria-selected", "true");
    expect(editor()).toHaveValue("SELECT 2");
  });

  test("fall back to the first tab when the one remembered is gone", () => {
    window.localStorage.setItem(activeTabStorageKey("c1"), "closed-elsewhere");

    show();

    expect(tab("Query 1")).toHaveAttribute("aria-selected", "true");
  });

  test("remember the tab that was switched to", () => {
    show();

    fireEvent.click(tab("Suspects"));

    expect(window.localStorage.getItem(activeTabStorageKey("c1"))).toBe("t2");
    expect(editor()).toHaveValue("SELECT 2");
  });

  test("save what was typed into the tab it was typed in", async () => {
    show();

    type("SELECT * FROM suspects");
    await wait(1500);

    const saves = requests("PATCH");
    expect(saves).toHaveLength(1);
    expect(saves[0].url).toBe("/api/v1/contests/c1/play/tabs/t1");
    expect(JSON.parse(String(saves[0].init.body))).toEqual({ body: "SELECT * FROM suspects" });
    expect(status()).toBe(t.status.saved);
  });

  test("save each tab on its own path", async () => {
    show();
    fireEvent.click(tab("Suspects"));

    type("SELECT * FROM alibis");
    await wait(1500);

    expect(requests("PATCH").map((call) => call.url)).toEqual(["/api/v1/contests/c1/play/tabs/t2"]);
  });

  test("open a new tab on the server and show it", async () => {
    show();

    fireEvent.click(screen.getByRole("button", { name: t.newTab }));
    await settle();

    expect(requests("POST")[0].url).toBe("/api/v1/contests/c1/play/tabs");
    expect(tab("Query 2")).toHaveAttribute("aria-selected", "true");
    expect(editor()).toHaveValue("");
  });

  test("rename a tab, and say so when the server refuses the name", async () => {
    show();

    fireEvent.doubleClick(tab("Suspects"));
    const field = screen.getByRole("textbox", { name: t.rename.replace("{tab}", "Suspects") });
    fireEvent.change(field, { target: { value: "Witnesses" } });
    fireEvent.keyDown(field, { key: "Enter" });
    await settle();

    const renames = requests("PATCH");
    expect(renames).toHaveLength(1);
    expect(JSON.parse(String(renames[0].init.body))).toEqual({ title: "Witnesses" });
    expect(tab("Witnesses")).toBeInTheDocument();

    answer = () => refusal(400, "workspace_title_invalid");
    fireEvent.doubleClick(tab("Witnesses"));
    const again = screen.getByRole("textbox", { name: t.rename.replace("{tab}", "Witnesses") });
    fireEvent.change(again, { target: { value: "" } });
    fireEvent.keyDown(again, { key: "Enter" });
    await settle();

    expect(status()).toBe(en.errors.workspace_title_invalid);
    expect(tab("Witnesses")).toBeInTheDocument();
  });
});

describe("closing a tab", () => {
  test("asks before closing a tab that holds text, and does nothing when the answer is no", async () => {
    confirmClose = vi.fn<(title: string) => boolean>(() => false);
    show();

    fireEvent.click(screen.getByRole("button", { name: t.close.replace("{tab}", "Suspects") }));
    await settle();

    expect(confirmClose).toHaveBeenCalledWith("Suspects");
    expect(requests("DELETE")).toHaveLength(0);
    expect(tab("Suspects")).toBeInTheDocument();
  });

  test("closes an empty tab at once, without asking", async () => {
    show();
    fireEvent.click(tab("Suspects"));
    type("");
    await wait(1500);
    calls = [];

    fireEvent.click(screen.getByRole("button", { name: t.close.replace("{tab}", "Suspects") }));
    await settle();

    expect(confirmClose).not.toHaveBeenCalled();
    expect(requests("DELETE")[0].url).toBe("/api/v1/contests/c1/play/tabs/t2");
    expect(screen.queryByRole("tab", { name: "Suspects" })).not.toBeInTheDocument();
  });

  // A closed tab's autosave has to stop: otherwise it keeps a timer, keeps a
  // draft, and sends one more save for a tab the server no longer has.
  test("stops the closed tab's autosave and forgets its draft", async () => {
    show();
    fireEvent.click(tab("Suspects"));
    type("SELECT * FROM alibis");
    await wait(400);
    expect(window.localStorage.getItem(draftStorageKey("c1", "tab:t2"))).not.toBeNull();
    calls = [];

    fireEvent.click(screen.getByRole("button", { name: t.close.replace("{tab}", "Suspects") }));
    await settle();
    await wait(30_000);

    expect(requests("PATCH")).toHaveLength(0);
    expect(window.localStorage.getItem(draftStorageKey("c1", "tab:t2"))).toBeNull();
    expect(dropped).toEqual(["t2"]);
    expect(tab("Query 1")).toHaveAttribute("aria-selected", "true");
  });
});

describe("reordering the tabs", () => {
  test("sends the new order", async () => {
    show();

    fireEvent.dragStart(tab("Suspects"));
    fireEvent.drop(tab("Query 1"));
    await settle();

    const order = requests("PUT");
    expect(order[0].url).toBe("/api/v1/contests/c1/play/tabs/order");
    expect(JSON.parse(String(order[0].init.body))).toEqual({ ids: ["t2", "t1"] });
    expect(screen.getAllByRole("tab").map((el) => el.getAttribute("aria-label"))).toEqual([
      "Suspects",
      "Query 1",
    ]);
  });

  test("puts the strip back when the server refuses the new order", async () => {
    show();
    answer = () => refusal(409, "workspace_order_mismatch");

    fireEvent.dragStart(tab("Suspects"));
    fireEvent.drop(tab("Query 1"));
    await settle();

    expect(screen.getAllByRole("tab").map((el) => el.getAttribute("aria-label"))).toEqual([
      "Query 1",
      "Suspects",
    ]);
    expect(status()).toBe(en.errors.workspace_order_mismatch);
  });
});

describe("a draft from before a reload", () => {
  test("is shown in its own tab and saved, even when that tab is not the one open", async () => {
    window.localStorage.setItem(
      draftStorageKey("c1", "tab:t2"),
      JSON.stringify({ text: "SELECT typed before the reload", base: "v0" }),
    );
    show();
    await settle();

    expect(restored).toEqual([["t2", "SELECT typed before the reload"]]);
    expect(requests("PATCH")[0].url).toBe("/api/v1/contests/c1/play/tabs/t2");
  });
});

describe("a contest that has ended", () => {
  test("stops saving, keeps the draft and says the contest is over", async () => {
    answer = () => refusal(409, "contest_finished");
    show();

    type("SELECT * FROM suspects");
    await wait(1500);

    expect(status()).toBe(t.status.closed);
    expect(screen.queryByRole("button", { name: t.newTab })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Close/ })).not.toBeInTheDocument();
    expect(JSON.parse(window.localStorage.getItem(draftStorageKey("c1", "tab:t1")) ?? "null")).toMatchObject({
      text: "SELECT * FROM suspects",
    });

    calls = [];
    type("SELECT again");
    await wait(30_000);
    expect(calls).toHaveLength(0);
  });
});

describe("a workspace that could not be read", () => {
  test("gives one tab that works and says nothing is saved", async () => {
    show(null);

    expect(screen.getAllByRole("tab")).toHaveLength(1);
    expect(tab(t.local)).toBeInTheDocument();
    expect(status()).toBe(t.unsaved);

    type("SELECT 1");
    await wait(30_000);

    expect(calls).toHaveLength(0);
  });
});
