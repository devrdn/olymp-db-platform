import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import type { Revision, RevisionInfo, Workspace } from "@/lib/api/monitor";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

const { fetchRevision } = vi.hoisted(() => ({ fetchRevision: vi.fn() }));
vi.mock("@/lib/api/monitor", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/monitor")>()),
  fetchRevision,
}));
// Every revision row formats its size once per render, so counting the calls
// counts the rows that rendered.
const { readableBytes } = vi.hoisted(() => ({ readableBytes: vi.fn() }));
vi.mock("@/lib/format/bytes", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/format/bytes")>();
  readableBytes.mockImplementation(actual.readableBytes);
  return { ...actual, readableBytes };
});

import { CONTEST, REG } from "./test-fixtures";
import { WorkspaceTab } from "./workspace-tab";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

const TAB = "7d1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";
const CLOSED = "8e1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";

function info(id: number, document: string, title = "", minute = id): RevisionInfo {
  const at = `2026-09-20T10:${String(minute).padStart(2, "0")}:00.000Z`;
  return { id, document, title, startedAt: at, updatedAt: at, size: 10 * id };
}

const BODIES: Record<number, string> = {
  1: "suspects:\nmaid\ncook",
  2: "SELECT 1",
  4: "suspects:\nbutler\ncook",
  5: "SELECT 1",
  6: "SELECT * FROM guests",
};

// Newest first, as the API lists them.
const workspace: Workspace = {
  notes: { body: "suspects:\nbutler\ncook", updatedAt: "2026-09-20T10:04:00.000Z" },
  tabs: [{ id: TAB, title: "Guests", position: 0, body: "SELECT 1", updatedAt: "2026-09-20T10:05:00.000Z" }],
  revisions: [
    info(6, CLOSED, "Scratch"),
    info(5, TAB, "Guests"),
    info(4, "notes"),
    info(2, TAB, "Query 1"),
    info(1, "notes"),
  ],
  truncated: false,
};

beforeEach(() => {
  fetchRevision.mockReset();
  fetchRevision.mockImplementation(async (_c: string, _r: string, id: number): Promise<Revision> => {
    const listed = workspace.revisions.find((r) => r.id === id) as RevisionInfo;
    return { ...listed, body: BODIES[id] };
  });
});

const t = () => dict.workspace.monitor.participant.workspace;

function renderTab(value: Workspace = workspace) {
  return render(<WorkspaceTab contestId={CONTEST} registrationId={REG} workspace={value} dict={dict} locale="en" />);
}

async function pick(document: string, index: number) {
  const group = screen.getByRole("group", { name: document });
  await act(async () => {
    fireEvent.click(within(group).getAllByRole("button")[index]);
  });
}

describe("the workspace as it is now", () => {
  test("shows the notes and every open tab", () => {
    renderTab();
    const now = screen.getByRole("region", { name: t().now });
    expect(within(now).getByText(/butler/)).toBeInTheDocument();
    expect(within(now).getByRole("heading", { name: "Guests" })).toBeInTheDocument();
    expect(within(now).getByText("SELECT")).toBeInTheDocument();
  });
});

describe("the history", () => {
  test("lists revisions per document, the closed tab's too", () => {
    renderTab();
    expect(within(screen.getByRole("group", { name: t().notes })).getAllByRole("button")).toHaveLength(2);
    expect(within(screen.getByRole("group", { name: "Guests" })).getAllByRole("button")).toHaveLength(2);
    expect(screen.getByRole("group", { name: `Scratch (${t().closedTab})` })).toBeInTheDocument();
    expect(screen.getByText(t().pick)).toBeInTheDocument();
  });

  test("a revision shows what changed since the previous one of the same document", async () => {
    renderTab();
    await pick(t().notes, 0);

    expect(fetchRevision).toHaveBeenCalledWith(CONTEST, REG, 4, expect.anything());
    expect(fetchRevision).toHaveBeenCalledWith(CONTEST, REG, 1, expect.anything());
    const diff = screen.getByRole("list", { name: t().changes });
    const lines = within(diff)
      .getAllByRole("listitem")
      .map((li) => `${li.dataset.type} ${li.querySelector("[data-text]")?.textContent ?? ""}`);
    expect(lines).toEqual(["same suspects:", "removed maid", "added butler", "same cook"]);
    expect(screen.getByText("+1 −1")).toBeInTheDocument();
  });

  /** The numbers the eye reads in the margin are hidden from a screen reader; a changed line says its own. */
  test("a changed line tells a screen reader which line it is", async () => {
    renderTab();
    await pick(t().notes, 0);

    expect(screen.getByText(t().removed.replace("{n}", "2"))).toHaveClass("sr-only");
    expect(screen.getByText(t().added.replace("{n}", "2"))).toHaveClass("sr-only");
    expect(t().added).toContain("{n}");
  });

  test("the first revision of a document is all new", async () => {
    renderTab();
    await pick(t().notes, 1);

    expect(fetchRevision).toHaveBeenCalledTimes(1);
    expect(screen.getByText(t().first)).toBeInTheDocument();
    const diff = screen.getByRole("list", { name: t().changes });
    expect(within(diff).getAllByRole("listitem").map((li) => li.dataset.type)).toEqual(["added", "added", "added"]);
  });

  test("a revision identical to the previous says so", async () => {
    renderTab();
    await pick("Guests", 0);

    expect(fetchRevision).toHaveBeenCalledWith(CONTEST, REG, 2, expect.anything());
    expect(screen.getByText(t().identical)).toBeInTheDocument();
  });

  test("shows the revision's own text on request", async () => {
    renderTab();
    await pick(`Scratch (${t().closedTab})`, 0);

    fireEvent.click(screen.getByRole("button", { name: t().body }));
    expect(screen.getByRole("region", { name: t().body })).toHaveTextContent("SELECT * FROM guests");
  });

  test("says when a revision could not be read", async () => {
    fetchRevision.mockRejectedValue(new Error("down"));
    renderTab();
    await pick(t().notes, 1);
    expect(screen.getByText(t().failed)).toBeInTheDocument();
  });

  test("says when nothing was saved yet", () => {
    renderTab({ ...workspace, revisions: [] });
    expect(screen.getByText(t().noHistory)).toBeInTheDocument();
  });
});

describe("choosing among many revisions", () => {
  /**
   * The list can hold two thousand revisions; choosing one, or switching
   * between the diff and the text, must not lay every row out again.
   */
  test("re-renders only the rows whose selection changed", async () => {
    renderTab();
    await pick(t().notes, 0);
    readableBytes.mockClear();

    await pick("Guests", 1);
    expect(readableBytes).toHaveBeenCalledTimes(2);

    readableBytes.mockClear();
    fireEvent.click(screen.getByRole("button", { name: t().body }));
    expect(readableBytes).not.toHaveBeenCalled();
  });
});
