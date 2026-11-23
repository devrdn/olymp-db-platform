import { redirect } from "next/navigation";
import { afterEach, beforeEach, describe, expect, expectTypeOf, test, vi } from "vitest";
import { z } from "zod";

import { ApiError } from "./client";

// React memoises `cache()` only inside a server render; this stand-in keeps one
// memo per test and compares arguments by identity, as React does.
const { serverRequest, memos } = vi.hoisted(() => ({ serverRequest: vi.fn(), memos: [] as unknown[][] }));
vi.mock("react", async (importOriginal) => ({
  ...(await importOriginal<typeof import("react")>()),
  cache: <A extends unknown[], R>(fn: (...args: A) => R) => {
    const memo: { args: A; value: R }[] = [];
    memos.push(memo);
    return (...args: A): R => {
      const hit = memo.find((entry) => entry.args.every((a, i) => Object.is(a, args[i])));
      if (hit) return hit.value;
      const value = fn(...args);
      memo.push({ args, value });
      return value;
    };
  },
}));
vi.mock("./server", () => ({ serverRequest }));

import { bestEffort, readPage, Refusal, type ReadPolicy } from "./page-read";

const PATH = "/contests/c1/questions/q1";
const HERE = "/contests/c1/questions";

const asIs = (payload: unknown) => payload;

beforeEach(() => {
  serverRequest.mockReset();
  for (const memo of memos) memo.length = 0;
});

afterEach(() => {
  vi.restoreAllMocks();
});

/** What a page sees of one read: its value, or which way Next was sent. */
async function seen(policy: Omit<ReadPolicy, "here">): Promise<string> {
  try {
    const value = await readPage(PATH, asIs, { here: HERE, ...policy });
    if (value === null) return "null";
    if (value instanceof Refusal) return `refusal ${value.code}`;
    return `value ${JSON.stringify(value)}`;
  } catch (error: unknown) {
    const digest = (error as { digest?: unknown }).digest;
    if (typeof digest === "string" && digest.startsWith("NEXT_REDIRECT")) return `redirect ${digest.split(";")[2]}`;
    if (typeof digest === "string" && digest.startsWith("NEXT_HTTP_ERROR_FALLBACK;404")) return "notFound";
    if (error instanceof ApiError) return `throws ${error.code}`;
    throw error;
  }
}

describe("a page's read, by the code the API answered and the page's policy", () => {
  test.each<[string, number, Omit<ReadPolicy, "here">, string]>([
    // A dead session or a one-time password: whatever the policy, a retry fixes neither.
    ["unauthenticated", 401, {}, "redirect /login?next=%2Fcontests%2Fc1%2Fquestions"],
    ["unauthenticated", 401, { bestEffort: true }, "redirect /login?next=%2Fcontests%2Fc1%2Fquestions"],
    ["password_change_required", 403, { absent: ["password_change_required"] }, "redirect /password"],

    // Not found is one code, never a status.
    ["not_found", 404, {}, "notFound"],
    ["not_found", 404, { absent: ["not_found"] }, "null"],
    ["not_found", 404, { bestEffort: true }, "null"],
    ["unreachable", 404, {}, "throws unreachable"],
    ["question_not_found", 404, {}, "throws question_not_found"],
    ["question_not_found", 404, { missing: ["question_not_found"] }, "notFound"],

    // `forbidden` hides existence only where the page says so.
    ["forbidden", 403, {}, "throws forbidden"],
    ["forbidden", 403, { hideForbidden: true }, "notFound"],
    ["forbidden", 403, { hideForbidden: true, absent: ["forbidden"] }, "null"],

    // A refusal the page shows is named by its code, not by a 429.
    ["archive_too_often", 429, { shown: ["archive_too_often"] }, "refusal archive_too_often"],
    ["archive_too_often", 429, {}, "throws archive_too_often"],
    ["unreachable", 429, { shown: ["archive_too_often"] }, "throws unreachable"],

    // Anything else is the error boundary, unless the page renders without it.
    ["internal_error", 500, {}, "throws internal_error"],
    ["internal_error", 500, { hideForbidden: true, missing: ["question_not_found"] }, "throws internal_error"],
    ["internal_error", 500, { bestEffort: true }, "null"],
  ])("%s (%i) under %j is %s", async (code, status, policy, expected) => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    vi.spyOn(console, "error").mockImplementation(() => {});
    serverRequest.mockRejectedValue(new ApiError(code, status, "", "req-1"));

    expect(await seen(policy)).toBe(expected);
  });

  test("gives the parsed answer when the API answered", async () => {
    serverRequest.mockResolvedValue({ id: "q1" });

    const value = await readPage(PATH, (payload) => z.object({ id: z.string() }).parse(payload), { here: HERE });

    expect(value).toEqual({ id: "q1" });
  });
});

describe("a best-effort read", () => {
  test("lets Next's own control flow through, a redirect thrown beneath it included", async () => {
    serverRequest.mockImplementation(async () => redirect("/elsewhere"));

    await expect(readPage(PATH, asIs, { here: HERE, bestEffort: true })).rejects.toMatchObject({
      digest: expect.stringContaining("NEXT_REDIRECT"),
    });
    await expect(bestEffort(async () => redirect("/elsewhere"), "/auth/me")).rejects.toMatchObject({
      digest: expect.stringContaining("NEXT_REDIRECT"),
    });
  });

  test("is null on an answer it cannot read, which a required read sends to the error boundary", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    serverRequest.mockResolvedValue({ id: 42 });
    const parse = (payload: unknown) => z.object({ id: z.string() }).parse(payload);

    expect(await readPage(PATH, parse, { here: HERE, bestEffort: true })).toBeNull();
    await expect(readPage(PATH, parse, { here: HERE })).rejects.toBeInstanceOf(z.ZodError);
  });

  test("logs each failure once, on one line, by its path and never its message", async () => {
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});
    serverRequest.mockRejectedValue(new TypeError("dbcontest_session=secret-token\nFAKE"));

    await readPage(PATH, asIs, { here: HERE, bestEffort: true });
    await bestEffort(async () => {
      throw new TypeError("dbcontest_session=secret-token\nFAKE");
    }, "/auth/me");

    expect(logged).toHaveBeenCalledTimes(2);
    const lines = logged.mock.calls.map((call) => call.map(String).join(" "));
    expect(lines[0]).toContain(PATH);
    expect(lines[1]).toContain("/auth/me");
    for (const line of lines) {
      expect(line).not.toContain("secret-token");
      expect(line).not.toMatch(/[\r\n]/);
    }
  });

  test("does not log an absence the page declared ordinary", async () => {
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});
    serverRequest.mockRejectedValue(new ApiError("forbidden", 403, ""));

    expect(await readPage(PATH, asIs, { here: HERE, bestEffort: true, absent: ["forbidden"] })).toBeNull();
    expect(logged).not.toHaveBeenCalled();
  });

  test.each<[string, Omit<ReadPolicy, "here">]>([
    ["not_found", {}],
    ["forbidden", { hideForbidden: true }],
    ["question_not_found", { missing: ["question_not_found"] }],
  ])("does not log %s where a required read would be the not-found page", async (code, policy) => {
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});
    serverRequest.mockRejectedValue(new ApiError(code, 404, ""));

    expect(await readPage(PATH, asIs, { here: HERE, bestEffort: true, ...policy })).toBeNull();
    expect(logged).not.toHaveBeenCalled();
  });

  test("gives the value of a call that worked", async () => {
    expect(await bestEffort(async () => "who", "/auth/me")).toBe("who");
  });
});

describe("one render's reads", () => {
  test("ask the API once for a path, whatever each caller parses and however each answers a refusal", async () => {
    serverRequest.mockResolvedValue({ problems: [] });

    const [layout, page] = await Promise.all([
      readPage(PATH, (payload) => ({ layout: payload }), { here: HERE, bestEffort: true }),
      readPage(PATH, (payload) => ({ page: payload }), { here: HERE, hideForbidden: true }),
    ]);

    expect(serverRequest).toHaveBeenCalledTimes(1);
    expect(layout).toEqual({ layout: { problems: [] } });
    expect(page).toEqual({ page: { problems: [] } });
  });
});

describe("the type of a read's answer", () => {
  test("follows a literal policy, and keeps every answer a wider one might give", () => {
    const parse = (payload: unknown) => String(payload);

    expectTypeOf(readPage(PATH, parse, { here: HERE })).resolves.toEqualTypeOf<string>();
    expectTypeOf(readPage(PATH, parse, { here: HERE, absent: ["x"] })).resolves.toEqualTypeOf<string | null>();
    expectTypeOf(readPage(PATH, parse, { here: HERE, bestEffort: true })).resolves.toEqualTypeOf<string | null>();
    expectTypeOf(readPage(PATH, parse, { here: HERE, shown: ["x"] })).resolves.toEqualTypeOf<string | Refusal>();

    const wide: ReadPolicy = { here: HERE };
    expectTypeOf(readPage(PATH, parse, wide)).resolves.toEqualTypeOf<string | null | Refusal>();
  });
});
