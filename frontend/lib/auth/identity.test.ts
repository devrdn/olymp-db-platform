import { redirect } from "next/navigation";
import { afterEach, describe, expect, test, vi } from "vitest";

// React memoises `cache()` only inside a server render; this stand-in keeps
// one memo for the file, which each test empties.
const { fetchIdentity, memos } = vi.hoisted(() => ({ fetchIdentity: vi.fn(), memos: [] as unknown[][] }));
vi.mock("react", async (importOriginal) => ({
  ...(await importOriginal<typeof import("react")>()),
  cache: <R>(fn: () => R) => {
    const memo: R[] = [];
    memos.push(memo);
    return (): R => {
      if (memo.length === 0) memo.push(fn());
      return memo[0];
    };
  },
}));
vi.mock("./session", () => ({ fetchIdentity }));

import { readIdentity } from "./identity";

afterEach(() => {
  fetchIdentity.mockReset();
  for (const memo of memos) memo.length = 0;
  vi.restoreAllMocks();
});

describe("readIdentity", () => {
  test("asks who is signed in once, and logs an outage once, however many parts of the screen ask", async () => {
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});
    fetchIdentity.mockRejectedValue(new Error("/auth/me could not be reached (status 502)"));

    expect(await Promise.all([readIdentity(), readIdentity(), readIdentity()])).toEqual([null, null, null]);
    expect(fetchIdentity).toHaveBeenCalledTimes(1);
    expect(logged).toHaveBeenCalledTimes(1);
  });

  test("lets Next's own control flow through", async () => {
    fetchIdentity.mockImplementation(async () => redirect("/elsewhere"));

    await expect(readIdentity()).rejects.toMatchObject({ digest: expect.stringContaining("NEXT_REDIRECT") });
  });
});
