import { beforeEach, describe, expect, test, vi } from "vitest";

// `vi.mock` factories are hoisted above every import in this file, so the
// mocks they return have to be built through `vi.hoisted` rather than closed
// over plain top-level `const`s — those would not exist yet when the factory
// actually runs.
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
 * `addParticipantAction` is the picker's own path: a person chosen from the
 * directory search arrives here as an id, not as a login the server has to
 * resolve a second time. It has to reach the same endpoint the bulk import
 * uses (`POST /contests/:id/participants`), carrying `user_ids` — the field
 * `importParticipantsAction` never sends, since that one only ever sends
 * `logins` typed by hand.
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
    // What the picker's hidden field holds before anybody has chosen a
    // candidate — the field the form still submits if the button is reached
    // without picking anyone.
    const state = await addParticipantAction({}, form({ contestId, userId: "" }));

    expect(state).toEqual({ code: "invalid_user_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  /**
   * The endpoint this shares with the roster import always answers 200 with
   * `{ added, skipped }` — it has no 409 for "already enrolled", because
   * AddParticipants.addOne catches that case itself and reports it as a
   * skip (see contests.SkipAlreadyEnrolled). A candidate the picker offered
   * can still fail to land — enrolled by somebody else a moment earlier —
   * and that has to read as something other than a plain success.
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
