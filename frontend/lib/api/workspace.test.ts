import { afterEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "./client";
import {
  createTab,
  deleteTab,
  reorderTabs,
  saveNotes,
  updateTab,
  workspaceSchema,
  workspaceTabSchema,
} from "./workspace";

const tab = {
  id: "5f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6",
  title: "Query 1",
  body: "SELECT 1",
  position: 0,
  updated_at: "2026-09-17T10:00:00Z",
};

describe("workspaceSchema", () => {
  test("reads the notes and the tabs into the interface's own names", () => {
    const parsed = workspaceSchema.parse({
      notes: { body: "the butler", updated_at: "2026-09-17T10:01:00Z" },
      tabs: [tab],
    });

    expect(parsed).toEqual({
      notes: { body: "the butler", updatedAt: "2026-09-17T10:01:00Z" },
      tabs: [{ id: tab.id, title: "Query 1", body: "SELECT 1", position: 0, updatedAt: "2026-09-17T10:00:00Z" }],
    });
  });

  test("keeps never-saved notes as a null version", () => {
    const parsed = workspaceSchema.parse({ notes: { body: "", updated_at: null }, tabs: [tab] });

    expect(parsed.notes.updatedAt).toBeNull();
  });
});

describe("workspaceTabSchema", () => {
  test("refuses a tab without an identifier", () => {
    expect(() => workspaceTabSchema.parse({ ...tab, id: undefined })).toThrow();
  });
});

describe("the browser-side writes", () => {
  const calls: { url: string; init: RequestInit }[] = [];

  function answer(status: number, body?: unknown) {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init: RequestInit) => {
        calls.push({ url, init });
        return body === undefined
          ? new Response(null, { status })
          : new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
      }),
    );
  }

  afterEach(() => {
    calls.length = 0;
    vi.unstubAllGlobals();
  });

  test("saves the notes to the same-origin API with the session cookie", async () => {
    answer(200, { updated_at: "2026-09-17T10:02:00Z" });

    await expect(saveNotes("c1", "the gardener")).resolves.toEqual({ updatedAt: "2026-09-17T10:02:00Z" });

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe("/api/v1/contests/c1/play/notes");
    expect(calls[0].init).toMatchObject({ method: "PUT", credentials: "same-origin", keepalive: false });
    expect(JSON.parse(String(calls[0].init.body))).toEqual({ body: "the gardener" });
  });

  test("asks for keepalive when the page may be closing", async () => {
    answer(200, { updated_at: "2026-09-17T10:02:00Z" });

    await saveNotes("c1", "x", { keepalive: true });

    expect(calls[0].init.keepalive).toBe(true);
  });

  test("raises the server's refusal as an ApiError", async () => {
    answer(409, { error: { code: "contest_finished", message: "over" } });

    await expect(saveNotes("c1", "x")).rejects.toBeInstanceOf(ApiError);
  });

  test("creates a tab with an empty object when it has no title", async () => {
    answer(201, tab);

    await expect(createTab("c1")).resolves.toMatchObject({ id: tab.id, title: "Query 1" });

    expect(calls[0].url).toBe("/api/v1/contests/c1/play/tabs");
    expect(calls[0].init.method).toBe("POST");
    expect(JSON.parse(String(calls[0].init.body))).toEqual({});
  });

  test("creates a tab with the title it was given", async () => {
    answer(201, tab);

    await createTab("c1", "Suspects");

    expect(JSON.parse(String(calls[0].init.body))).toEqual({ title: "Suspects" });
  });

  test("patches only the fields it was given", async () => {
    answer(200, { updated_at: "2026-09-17T10:03:00Z" });

    await expect(updateTab("c1", tab.id, { body: "SELECT 2" }, { keepalive: true })).resolves.toEqual({
      updatedAt: "2026-09-17T10:03:00Z",
    });

    expect(calls[0].url).toBe(`/api/v1/contests/c1/play/tabs/${tab.id}`);
    expect(calls[0].init).toMatchObject({ method: "PATCH", keepalive: true });
    expect(JSON.parse(String(calls[0].init.body))).toEqual({ body: "SELECT 2" });
  });

  test("deletes a tab", async () => {
    answer(204);

    await expect(deleteTab("c1", tab.id)).resolves.toBeUndefined();

    expect(calls[0]).toMatchObject({
      url: `/api/v1/contests/c1/play/tabs/${tab.id}`,
      init: { method: "DELETE", credentials: "same-origin" },
    });
  });

  test("sends the whole new order", async () => {
    answer(204);

    await reorderTabs("c1", ["b", "a"]);

    expect(calls[0].url).toBe("/api/v1/contests/c1/play/tabs/order");
    expect(calls[0].init.method).toBe("PUT");
    expect(JSON.parse(String(calls[0].init.body))).toEqual({ ids: ["b", "a"] });
  });

  // An identifier comes from the server, but it is still put in a path.
  test("escapes the identifiers it puts in a path", async () => {
    answer(204);

    await deleteTab("c/1", "a?b");

    expect(calls[0].url).toBe("/api/v1/contests/c%2F1/play/tabs/a%3Fb");
  });
});
