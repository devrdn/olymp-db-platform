import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";

const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
// The wire format is `contests.test.ts`'s business.
vi.mock("@/lib/api/contests", () => ({ contestSchema: { parse: (payload: unknown) => payload } }));

import { loadContest, loadContestResource } from "./contest";

const ID = "f767af3b-f135-40d2-a3a6-82d368de1004";

const asIs = (payload: unknown) => payload;

beforeEach(() => {
  serverRequest.mockReset();
});

afterEach(() => {
  vi.restoreAllMocks();
});

const NOT_FOUND = { digest: expect.stringContaining("NEXT_HTTP_ERROR_FALLBACK;404") };

describe("the workspace's reads of a contest", () => {
  test("never turn a segment that is not an id into a request", async () => {
    await expect(loadContest("x\nFAKE")).rejects.toMatchObject(NOT_FOUND);
    await expect(loadContestResource("x\nFAKE", "/questions", asIs)).rejects.toMatchObject(NOT_FOUND);
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("answer a contest the caller may not see as one that does not exist", async () => {
    serverRequest.mockRejectedValue(new ApiError("forbidden", 403, ""));

    await expect(loadContest(ID)).rejects.toMatchObject(NOT_FOUND);
    await expect(loadContestResource(ID, "/questions", asIs)).rejects.toMatchObject(NOT_FOUND);
  });

  test("send a dead session to sign-in, back to the contest", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    serverRequest.mockRejectedValue(new ApiError("unauthenticated", 401, ""));

    await expect(loadContestResource(ID, "/monitor/participants", asIs)).rejects.toMatchObject({
      digest: expect.stringContaining(`/login?next=${encodeURIComponent(`/contests/${ID}`)}`),
    });
  });

  test("read a sub-resource beneath the contest, under the screen's own policy", async () => {
    serverRequest.mockImplementation(async (path: string) => {
      if (path === `/contests/${ID}`) return { id: ID };
      throw new ApiError("story_not_found", 404, "");
    });

    expect(await loadContestResource(ID, "/story", asIs, { absent: ["story_not_found"] })).toBeNull();
    expect(serverRequest).toHaveBeenCalledWith(`/contests/${ID}/story`);
  });
});

// A content form pairs the contest's version with a copy of its content. Read
// in that order, a write landing between the two leaves a newer copy at an
// older version, which the save is refused at; read beside each other, an
// older copy could come with the newer version and be saved over it. The
// order is the loader's, so no screen can get it wrong.
describe("a read of a contest's content", () => {
  test("starts only once the contest is read, whatever the screen reads beside it", async () => {
    // Each read of the contest waits here; a render shares one (`readPage`).
    const contestReads: ((payload: unknown) => void)[] = [];
    serverRequest.mockImplementation((path: string) =>
      path === `/contests/${ID}` ? new Promise((resolve) => contestReads.push(resolve)) : Promise.resolve({ path }),
    );

    const loaded = Promise.all([
      loadContest(ID),
      loadContestResource(ID, "/story", asIs),
      loadContestResource(ID, "/schema-notes", asIs),
    ]);
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(serverRequest.mock.calls.map(([path]) => path).filter((path) => path !== `/contests/${ID}`)).toEqual([]);
    for (const read of contestReads) read({ id: ID, version: 3 });

    expect(await loaded).toEqual([
      { id: ID, version: 3 },
      { path: `/contests/${ID}/story` },
      { path: `/contests/${ID}/schema-notes` },
    ]);
  });
});
