import { afterEach, describe, expect, test, vi } from "vitest";

// Both read the current request, which does not exist here.
vi.mock("next/headers", () => ({ cookies: async () => ({ get: () => ({ value: "abc" }) }) }));
vi.mock("@/lib/api/caller", () => ({ callerHeaders: async () => ({}) }));

import { fetchIdentity, identityFrom } from "./session";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("fetchIdentity", () => {
  // Every signed-in layout waits on it, so a stalled API must not hold a
  // render for minutes.
  test("bounds how long the API may take to say who the caller is", async () => {
    let seen: RequestInit | undefined;
    vi.stubGlobal("fetch", async (_url: string, init?: RequestInit) => {
      seen = init;
      return new Response(JSON.stringify({ id: "u1", login: "ivanov" }), { status: 200 });
    });

    await fetchIdentity();

    expect(seen?.signal).toBeInstanceOf(AbortSignal);
    expect(seen?.signal?.aborted).toBe(false);
  });
});

describe("identityFrom", () => {
  test("reports who the caller is when the server says so", async () => {
    const identity = await identityFrom(
      new Response(
        JSON.stringify({ id: "u1", login: "ivanov", full_name: "Ivan Ivanov", roles: ["student"] }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    expect(identity).toMatchObject({ login: "ivanov", fullName: "Ivan Ivanov", roles: ["student"] });
  });

  test("reports no session when the server says the session is no good", async () => {
    const identity = await identityFrom(
      new Response(JSON.stringify({ error: { code: "unauthenticated" } }), { status: 401 }),
    );

    expect(identity).toBeNull();
  });

  test("refuses to call an outage a sign-out", async () => {
    await expect(identityFrom(new Response("", { status: 500 }))).rejects.toThrow();
  });

  test("refuses to call an unreachable server a sign-out either", async () => {
    await expect(identityFrom(null)).rejects.toThrow();
  });

  test("falls back to the login when the account carries no name yet", async () => {
    const identity = await identityFrom(
      new Response(JSON.stringify({ id: "u1", login: "s.popescu" }), { status: 200 }),
    );

    expect(identity).toMatchObject({ login: "s.popescu", fullName: "", roles: [] });
  });
});
