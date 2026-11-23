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
    serverRequest.mockRejectedValue(new ApiError("story_not_found", 404, ""));

    expect(await loadContestResource(ID, "/story", asIs, { absent: ["story_not_found"] })).toBeNull();
    expect(serverRequest).toHaveBeenCalledWith(`/contests/${ID}/story`);
  });
});
