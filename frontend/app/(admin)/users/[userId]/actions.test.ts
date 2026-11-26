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

import {
  blockAction,
  deleteAction,
  resetPasswordAction,
  restoreAction,
  unlinkIdentityAction,
  unlockSignInAction,
} from "./actions";

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
    expect(state).toEqual({ ok: true });
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
    expect(state).toEqual({ ok: true });
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
    expect(state).toEqual({ ok: true });
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
    expect(state).toEqual({ ok: true });
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

describe("resetPasswordAction", () => {
  test("posts with no body when the email box is not ticked, and returns the password", async () => {
    serverRequest.mockResolvedValueOnce({ one_time_password: "swordfish-1", emailed: false });

    const state = await resetPasswordAction({}, form({ userId }));

    expect(serverRequest).toHaveBeenCalledWith(`/users/${userId}/password-reset`, { method: "POST" });
    expect(state).toMatchObject({ ok: true, oneTimePassword: "swordfish-1", emailed: false });
    expect(revalidatePath).toHaveBeenCalledWith("/users", "layout");
  });

  test("asks for the sign-in details by email when the box is ticked, and says why it was not sent", async () => {
    serverRequest.mockResolvedValueOnce({
      one_time_password: "swordfish-1",
      emailed: false,
      email_skipped_reason: "no_address",
    });

    const state = await resetPasswordAction({}, form({ userId, send_by_email: "on" }));

    expect(serverRequest).toHaveBeenCalledWith(`/users/${userId}/password-reset`, {
      method: "POST",
      body: { send_by_email: true },
    });
    expect(state).toEqual({
      ok: true,
      oneTimePassword: "swordfish-1",
      emailed: false,
      emailSkippedReason: "no_address",
    });
  });

  test("passes the mail-off refusal through by its own code", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("mail_off", 409, "mail is off"));

    const state = await resetPasswordAction({}, form({ userId, send_by_email: "on" }));

    expect(state).toEqual({ code: "mail_off" });
    expect(revalidatePath).not.toHaveBeenCalled();
  });

  test("refuses an identifier that is not one without calling the server", async () => {
    const state = await resetPasswordAction({}, form({ userId: "../roles", send_by_email: "on" }));

    expect(state).toEqual({ code: "invalid_user_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });
});

describe("unlinkIdentityAction", () => {
  const identityId = "6f1c2d3e-4b5a-4c6d-8e7f-901a2b3c4d5e";

  test("deletes the identity from the account and revalidates", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    const state = await unlinkIdentityAction({}, form({ userId, identityId }));

    expect(serverRequest).toHaveBeenCalledWith(`/users/${userId}/identities/${identityId}`, { method: "DELETE" });
    expect(state).toEqual({ ok: true });
    expect(revalidatePath).toHaveBeenCalledWith("/users", "layout");
  });

  test("refuses an account identifier that is not one without calling the server", async () => {
    const state = await unlinkIdentityAction({}, form({ userId: "../roles", identityId }));

    expect(state).toEqual({ code: "invalid_user_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("refuses an identity identifier that is not one without calling the server", async () => {
    const state = await unlinkIdentityAction({}, form({ userId, identityId: "../../roles" }));

    expect(state).toEqual({ code: "invalid_request" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("reports an identity unlinked since the page loaded by its code", async () => {
    serverRequest.mockRejectedValueOnce(
      new ApiError("identity_not_found", 404, "This sign-in service is no longer linked to the account"),
    );

    expect(await unlinkIdentityAction({}, form({ userId, identityId }))).toEqual({ code: "identity_not_found" });
    expect(revalidatePath).not.toHaveBeenCalled();
  });
});
