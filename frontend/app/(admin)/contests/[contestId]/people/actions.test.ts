import { beforeEach, describe, expect, test, vi } from "vitest";

// `vi.mock` factories are hoisted, so their mocks are built with `vi.hoisted`.
const { revalidatePath, serverRequest } = vi.hoisted(() => ({
  revalidatePath: vi.fn(),
  serverRequest: vi.fn(),
}));
vi.mock("next/cache", () => ({ revalidatePath }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));

import { addParticipantAction } from "./actions";

const contestId = "11111111-1111-1111-1111-111111111111";
const userId = "22222222-2222-2222-2222-222222222222";

function form(fields: Record<string, string>): FormData {
  const data = new FormData();
  for (const [key, value] of Object.entries(fields)) data.set(key, value);
  return data;
}

beforeEach(() => {
  serverRequest.mockReset();
  revalidatePath.mockReset();
});

/**
 * The picker sends an id as `user_ids` to the import endpoint (`POST
 * /contests/:id/participants`); the bulk import only sends `logins`.
 */
describe("addParticipantAction", () => {
  test("sends the chosen candidate's id as user_ids, and revalidates the contest", async () => {
    serverRequest.mockResolvedValueOnce({ added: 1, skipped: [] });

    const state = await addParticipantAction({}, form({ contestId, userId }));

    expect(state).toEqual({ done: true });
    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/participants`, {
      method: "POST",
      body: { user_ids: [userId] },
    });
    expect(revalidatePath).toHaveBeenCalledWith(`/contests/${contestId}`, "layout");
  });

  test("refuses an empty selection without ever calling the server", async () => {
    // The hidden field before anyone is chosen.
    const state = await addParticipantAction({}, form({ contestId, userId: "" }));

    expect(state).toEqual({ code: "invalid_user_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  /**
   * The endpoint answers 200 with `{ added, skipped }`, reporting "already
   * enrolled" as a skip (contests.SkipAlreadyEnrolled), which must not read as
   * success.
   */
  test("reports the skip reason when the chosen candidate was not actually added", async () => {
    serverRequest.mockResolvedValueOnce({
      added: 0,
      skipped: [{ ref: userId, reason: "already_enrolled" }],
    });

    const state = await addParticipantAction({}, form({ contestId, userId }));

    expect(state).toEqual({ skipReason: "already_enrolled" });
    expect(revalidatePath).not.toHaveBeenCalled();
  });

  test("surfaces the server's own error code on a genuine failure", async () => {
    const { ApiError } = await import("@/lib/api/client");
    serverRequest.mockRejectedValueOnce(new ApiError("not_editable", 409, "not editable"));

    const state = await addParticipantAction({}, form({ contestId, userId }));

    expect(state).toEqual({ code: "not_editable" });
    expect(revalidatePath).not.toHaveBeenCalled();
  });
});
