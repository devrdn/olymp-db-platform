import { beforeEach, describe, expect, test, vi } from "vitest";

// React memoises `cache()` only inside a server render; this stand-in keeps one
// memo for the whole file and compares arguments by identity, as React does.
const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("react", async (importOriginal) => ({
  ...(await importOriginal<typeof import("react")>()),
  cache: <A extends unknown[], R>(fn: (...args: A) => R) => {
    const memo: { args: A; value: R }[] = [];
    return (...args: A): R => {
      const hit = memo.find((entry) => entry.args.length === args.length && entry.args.every((a, i) => Object.is(a, args[i])));
      if (hit) return hit.value;
      const value = fn(...args);
      memo.push({ args, value });
      return value;
    };
  },
}));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("next/navigation", () => ({ notFound: vi.fn(), redirect: vi.fn() }));
// The wire format is `contests.test.ts`'s business.
vi.mock("@/lib/api/contests", () => ({ contestSchema: { parse: (payload: unknown) => payload } }));

import { loadContest, loadContestResource } from "./contest";

const ID = "f767af3b-f135-40d2-a3a6-82d368de1004";

const contest = { id: ID, title: "Night in the archive" };

beforeEach(() => {
  serverRequest.mockReset();
});

describe("one render's reads of a contest", () => {
  test("ask the API for the contest once, however many parts of the screen need it", async () => {
    serverRequest.mockResolvedValue(contest);

    const [first, second] = await Promise.all([loadContest(ID), loadContest(ID)]);

    expect(serverRequest).toHaveBeenCalledTimes(1);
    expect(second).toEqual(first);
  });

  test("ask for a sub-resource once, whatever each caller parses it with", async () => {
    serverRequest.mockResolvedValue({ problems: [] });

    const [layout, page] = await Promise.all([
      loadContestResource(ID, "/publish-check", (payload) => ({ layout: payload })),
      loadContestResource(ID, "/publish-check", (payload) => ({ page: payload })),
    ]);

    expect(serverRequest).toHaveBeenCalledTimes(1);
    expect(layout).toEqual({ layout: { problems: [] } });
    expect(page).toEqual({ page: { problems: [] } });
  });
});
