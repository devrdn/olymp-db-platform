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

import { ApiError } from "@/lib/api/client";

import { blockAction, deleteAction, restoreAction, unlockSignInAction } from "./actions";

const userId = "9a1f0c3e-2b44-4e77-8d0a-1c5b8e91a4d6";

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
 * `blockAction` used to send no `reason` at all, which the server has
 * required since early in this branch (`users.ErrReasonRequired`) — every
 * block from the account card failed. These tests pin both halves of the
 * fix: an empty reason never reaches the server, and a real one is sent.
 */
describe("blockAction", () => {
  test("refuses an empty reason without ever calling the server", async () => {
    const state = await blockAction({}, form({ userId, reason: "  " }));

    expect(state).toEqual({ code: "reason_required" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("sends the reason to /block and succeeds — the defect this task fixes", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    const state = await blockAction({}, form({ userId, reason: "cheating in the October contest" }));

    expect(serverRequest).toHaveBeenCalledWith(`/users/${userId}/block`, {
      method: "POST",
      body: { reason: "cheating in the October contest" },
    });
    expect(state).toEqual({ done: true });
    expect(revalidatePath).toHaveBeenCalledWith("/users", "layout");
  });
});

describe("deleteAction", () => {
  test("refuses an empty reason without ever calling the server", async () => {
    const state = await deleteAction({}, form({ userId, reason: "   " }));

    expect(state).toEqual({ code: "reason_required" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("refuses a reason of only whitespace the same way it refuses an empty one", async () => {
    const state = await deleteAction({}, form({ userId, reason: "\t\n " }));

    expect(state.code).toBe("reason_required");
  });

  test("sends the reason to /delete and succeeds", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    const state = await deleteAction({}, form({ userId, reason: "graduated" }));

    expect(serverRequest).toHaveBeenCalledWith(`/users/${userId}/delete`, {
      method: "POST",
      body: { reason: "graduated" },
    });
    expect(state).toEqual({ done: true });
  });
});

/**
 * Restoring can fail because a live account has since taken the login or the
 * email — the direct price of releasing them on deletion. The two failures
 * carry different codes on the wire (`login_taken`, `email_taken`), and this
 * pins that `restoreAction` passes each straight through rather than
 * collapsing them into one generic failure — `Outcome` in `account-card.tsx`
 * looks the code up and shows the administrator which field to go fix.
 */
describe("restoreAction", () => {
  test("restores with no body — there is nothing else to say about it", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    const state = await restoreAction({}, form({ userId }));

    expect(serverRequest).toHaveBeenCalledWith(`/users/${userId}/restore`, { method: "POST" });
    expect(state).toEqual({ done: true });
  });

  test("names the login when a live account has since taken it", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("login_taken", 409, "This login is already in use"));

    const state = await restoreAction({}, form({ userId }));

    expect(state).toEqual({ code: "login_taken" });
  });

  test("names the email when a live account has since taken it — a different code, not the same one", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("email_taken", 409, "This email is already in use"));

    const state = await restoreAction({}, form({ userId }));

    expect(state).toEqual({ code: "email_taken" });
  });
});

describe("unlockSignInAction", () => {
  test("posts to the account's sign-in unlock and revalidates", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    const state = await unlockSignInAction({}, form({ userId }));

    expect(serverRequest).toHaveBeenCalledWith(`/users/${userId}/sign-in/unlock`, { method: "POST" });
    expect(state).toEqual({ done: true });
    expect(revalidatePath).toHaveBeenCalledWith("/users", "layout");
  });

  test("refuses an identifier that is not one without calling the server", async () => {
    const state = await unlockSignInAction({}, form({ userId: "../roles" }));

    expect(state).toEqual({ code: "invalid_user_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("reports the server's refusal by its code", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("not_found", 404, "User not found"));

    expect(await unlockSignInAction({}, form({ userId }))).toEqual({ code: "not_found" });
  });
});
