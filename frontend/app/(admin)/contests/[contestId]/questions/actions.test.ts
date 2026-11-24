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

import { addQuestionAction, deleteQuestionAction, reorderQuestionsAction } from "./actions";

const contestId = "11111111-1111-1111-1111-111111111111";
const questionId = "22222222-2222-2222-2222-222222222222";

function form(fields: Record<string, string | string[]>): FormData {
  const data = new FormData();
  for (const [key, value] of Object.entries(fields)) {
    for (const one of Array.isArray(value) ? value : [value]) data.append(key, one);
  }
  return data;
}

beforeEach(() => {
  serverRequest.mockReset();
  revalidatePath.mockReset();
});

// Every question write moves the contest's version, and the settings page
// sends the version back: each must leave that page to be read again.
describe.each([
  ["adding", () => addQuestionAction({}, form({ contestId }))],
  ["deleting", () => deleteQuestionAction({}, form({ contestId, questionId }))],
  ["reordering", () => reorderQuestionsAction({}, form({ contestId, order: [questionId] }))],
])("%s a question", (_, act) => {
  test("re-reads the contest's pages, the settings form's version among them", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    expect(await act()).toEqual({ ok: true });

    expect(revalidatePath).toHaveBeenCalledWith(`/contests/${contestId}`, "layout");
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
