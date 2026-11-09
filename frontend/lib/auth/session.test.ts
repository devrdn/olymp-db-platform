import { NextResponse } from "next/server";
import { afterEach, describe, expect, test, vi } from "vitest";

const jar = vi.hoisted(() => new Map<string, string>());

// Both read the current request, which does not exist here.
vi.mock("next/headers", () => ({
  cookies: async () => ({ get: (name: string) => (jar.has(name) ? { value: jar.get(name) } : undefined) }),
}));
vi.mock("@/lib/api/caller", () => ({ callerHeaders: async () => ({}) }));

import { fetchIdentity, identityFrom, sessionCookie, sessionHeader } from "./session";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
  jar.clear();
});

describe("sessionCookie", () => {
  test("takes the host-only prefix where the cookie is Secure, so no sibling host can set one in its place", () => {
    vi.stubEnv("COOKIE_SECURE", "true");

    expect(sessionCookie()).toEqual({ name: "__Host-dbcontest_session", path: "/", secure: true });
  });

  test("keeps the bare name over plain http, where a browser refuses the prefix", () => {
    vi.stubEnv("COOKIE_SECURE", "false");

    expect(sessionCookie()).toEqual({ name: "dbcontest_session", path: "/", secure: false });
  });

  // A browser ignores a prefixed cookie set without Secure, the expired one
  // that clears it included.
  test("clears the prefixed cookie with the attributes a browser demands of it", () => {
    vi.stubEnv("COOKIE_SECURE", "true");
    const response = NextResponse.next();

    response.cookies.delete(sessionCookie());

    const cleared = response.headers.get("set-cookie") ?? "";
    expect(cleared).toMatch(/^__Host-dbcontest_session=;/);
    expect(cleared).toContain("Path=/");
    expect(cleared).toContain("Secure");
  });
});

describe("sessionHeader", () => {
  test("passes the session on under the name the deployment gives it", async () => {
    vi.stubEnv("COOKIE_SECURE", "true");
    jar.set("__Host-dbcontest_session", "abc");

    expect(await sessionHeader()).toEqual({ cookie: "__Host-dbcontest_session=abc" });
  });

  test("passes on no session where the cookie lacks the prefix it must carry", async () => {
    vi.stubEnv("COOKIE_SECURE", "true");
    jar.set("dbcontest_session", "abc");

    expect(await sessionHeader()).toEqual({});
  });
});

describe("fetchIdentity", () => {
  // Every signed-in layout waits on it, so a stalled API must not hold a
  // render for minutes.
  test("bounds how long the API may take to say who the caller is", async () => {
    jar.set(sessionCookie().name, "abc");
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
