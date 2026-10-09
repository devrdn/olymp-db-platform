import { beforeEach, describe, expect, test, vi } from "vitest";

// `vi.mock` factories are hoisted, so their mocks are built with `vi.hoisted`.
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
 * The server requires a reason (`users.ErrReasonRequired`): an empty one never
 * leaves, a real one is sent.
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
 * `login_taken` and `email_taken` pass through separately, so `Outcome` can
 * name the field to fix.
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
