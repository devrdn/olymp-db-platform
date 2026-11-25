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

/** What the editor rendered at contest version 7 submits for a plain question. */
function questionForm(): FormData {
  return form([
    ["contestId", contestId],
    ["questionId", questionId],
    ["expectedVersion", "7"],
    ["kind", "text"],
    ["points", "5"],
    ["body.en", "Who did it?"],
    ["answerValue", "the butler"],
    ["answerKind", "exact_ci"],
  ]);
}

describe("saveQuestionAction", () => {
  // A question moves the contest's version, and every form of the contest
  // sends the version back: the workspace must be read again.
  test("sends the version it was rendered at, then re-reads the contest's pages", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    expect(await saveQuestionAction({}, questionForm())).toEqual({ ok: true });

    expect(serverRequest).toHaveBeenCalledWith(
      `/contests/${contestId}/questions/${questionId}`,
      expect.objectContaining({ method: "PUT", body: expect.objectContaining({ expected_version: 7 }) }),
    );
    expect(revalidatePath).toHaveBeenCalledWith(`/contests/${contestId}`, "layout");
  });

  test("refuses a form with no version without asking the server", async () => {
    const data = questionForm();
    data.delete("expectedVersion");

    expect(await saveQuestionAction({}, data)).toEqual({ code: "invalid_request" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("hands back the server's refusal of a contest that moved since", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("contest_changed", 409, "changed"));

    expect(await saveQuestionAction({}, questionForm())).toEqual({ code: "contest_changed" });
    expect(revalidatePath).not.toHaveBeenCalled();
  });

  test("never sends a request for an identifier that would move the path", async () => {
    const state = await saveQuestionAction({}, form([["contestId", contestId], ["questionId", ".."]]));

    expect(state).toEqual({ code: "invalid_question_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });
});
