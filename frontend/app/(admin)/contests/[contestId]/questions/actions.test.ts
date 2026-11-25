import { beforeEach, describe, expect, test, vi } from "vitest";

// `vi.mock` factories are hoisted, so their mocks are built with `vi.hoisted`.
const { revalidatePath, serverRequest } = vi.hoisted(() => ({
  revalidatePath: vi.fn(),
  serverRequest: vi.fn(),
}));
vi.mock("next/cache", () => ({ revalidatePath }));
vi.mock("@/lib/api/server", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/server")>()),
  serverRequest,
}));

import { ApiError } from "@/lib/api/client";

import { addQuestionAction, deleteQuestionAction, reorderQuestionsAction } from "./actions";

const contestId = "11111111-1111-1111-1111-111111111111";
const questionId = "22222222-2222-2222-2222-222222222222";

/** What a form rendered at contest version 7 submits. */
function form(fields: Record<string, string | string[]>): FormData {
  const data = new FormData();
  data.set("expectedVersion", "7");
  for (const [key, value] of Object.entries(fields)) {
    for (const one of Array.isArray(value) ? value : [value]) data.append(key, one);
  }
  return data;
}

beforeEach(() => {
  serverRequest.mockReset();
  revalidatePath.mockReset();
});

// Every question write moves the contest's version, and every form of the
// contest sends the version back: each must leave the workspace to be read
// again.
// The delete carries the version in its query string: a proxy may drop the
// body of a DELETE, and the guard would then fail open.
describe.each([
  ["adding", addQuestionAction, form({ contestId }), "POST", `/contests/${contestId}/questions`, "body"],
  [
    "deleting",
    deleteQuestionAction,
    form({ contestId, questionId }),
    "DELETE",
    `/contests/${contestId}/questions/${questionId}`,
    "query",
  ],
  [
    "reordering",
    reorderQuestionsAction,
    form({ contestId, order: [questionId] }),
    "PUT",
    `/contests/${contestId}/questions/order`,
    "body",
  ],
] as const)("%s a question", (_, action, data, method, path, versionIn) => {
  test("sends the version the list was rendered at, then re-reads the contest's pages", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    expect(await action({}, data)).toEqual({ ok: true });

    const [sentPath, init] = serverRequest.mock.calls[0] as [string, { method: string; body?: Record<string, unknown> }];
    expect(init.method).toBe(method);
    if (versionIn === "query") {
      expect(sentPath).toBe(`${path}?expected_version=7`);
      expect(init.body).toBeUndefined();
    } else {
      expect(sentPath).toBe(path);
      expect(init.body?.expected_version).toBe(7);
    }
    expect(revalidatePath).toHaveBeenCalledWith(`/contests/${contestId}`, "layout");
  });

  test("refuses a form with no version without asking the server", async () => {
    const unversioned = new FormData();
    for (const [key, value] of data.entries()) if (key !== "expectedVersion") unversioned.append(key, value);

    expect(await action({}, unversioned)).toEqual({ code: "invalid_request" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("hands back the server's refusal of a contest that moved since", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("contest_changed", 409, "changed"));

    expect(await action({}, data)).toEqual({ code: "contest_changed" });
    expect(revalidatePath).not.toHaveBeenCalled();
  });
});

describe("the question actions' identifiers", () => {
  test("never send a request for a contest segment that is not an identifier", async () => {
    expect(await addQuestionAction({}, form({ contestId: "../users" }))).toEqual({ code: "invalid_contest_id" });
    expect(await deleteQuestionAction({}, form({ contestId, questionId: ".." }))).toEqual({
      code: "invalid_question_id",
    });
    expect(serverRequest).not.toHaveBeenCalled();
  });
});
