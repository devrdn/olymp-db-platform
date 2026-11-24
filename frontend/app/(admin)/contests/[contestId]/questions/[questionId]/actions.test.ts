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

import { saveQuestionAction } from "./actions";

const contestId = "11111111-1111-1111-1111-111111111111";
const questionId = "22222222-2222-2222-2222-222222222222";

function form(fields: [string, string][]): FormData {
  const data = new FormData();
  for (const [key, value] of fields) data.append(key, value);
  return data;
}

beforeEach(() => {
  serverRequest.mockReset();
  revalidatePath.mockReset();
});

describe("saveQuestionAction", () => {
  // A question moves the contest's version, and the settings page sends the
  // version back: it must be read again.
  test("re-reads the contest's pages, the settings form's version among them", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    const state = await saveQuestionAction(
      {},
      form([
        ["contestId", contestId],
        ["questionId", questionId],
        ["kind", "text"],
        ["points", "5"],
        ["body.en", "Who did it?"],
        ["answerValue", "the butler"],
        ["answerKind", "exact_ci"],
      ]),
    );

    expect(state).toEqual({ ok: true });
    expect(serverRequest).toHaveBeenCalledWith(
      `/contests/${contestId}/questions/${questionId}`,
      expect.objectContaining({ method: "PUT" }),
    );
    expect(revalidatePath).toHaveBeenCalledWith(`/contests/${contestId}`, "layout");
  });

  test("never sends a request for an identifier that would move the path", async () => {
    const state = await saveQuestionAction({}, form([["contestId", contestId], ["questionId", ".."]]));

    expect(state).toEqual({ code: "invalid_question_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });
});
