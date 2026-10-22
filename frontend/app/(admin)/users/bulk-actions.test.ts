import { beforeEach, describe, expect, test, vi } from "vitest";

// `vi.mock` factories are hoisted, so mocks are built with `vi.hoisted`. Only
// `serverRequest` is mocked, so the real request bodies and zod parsing in
// `lib/api/accounts.ts` are exercised.
const { revalidatePath, serverRequest } = vi.hoisted(() => ({
  revalidatePath: vi.fn(),
  serverRequest: vi.fn(),
}));
vi.mock("next/cache", () => ({ revalidatePath }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));

import { ApiError } from "@/lib/api/client";

import { bulkResetPasswordAction } from "./bulk-actions";

const first = "9a1f0c3e-2b44-4e77-8d0a-1c5b8e91a4d6";
const second = "1a2b3c4d-2b44-4e77-8d0a-1c5b8e91a4d6";

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

describe("bulkResetPasswordAction", () => {
  test("sends only the ids when the email box is not ticked", async () => {
    serverRequest.mockResolvedValueOnce({ issued: [], skipped: [] });

    await bulkResetPasswordAction({}, form({ ids: [first, second] }));

    expect(serverRequest).toHaveBeenCalledWith("/users/bulk/password-reset", {
      method: "POST",
      body: { ids: [first, second] },
    });
  });

  test("asks for the sign-in details by email when the box is ticked, and returns each outcome", async () => {
    serverRequest.mockResolvedValueOnce({
      issued: [
        { id: first, login: "petrov", one_time_password: "p1", emailed: true },
        {
          id: second,
          login: "blocked1",
          one_time_password: "p2",
          emailed: false,
          email_skipped_reason: "not_active",
        },
      ],
      skipped: [],
    });

    const state = await bulkResetPasswordAction(
      {},
      form({ ids: [first, second], send_by_email: "on" }),
    );

    expect(serverRequest).toHaveBeenCalledWith("/users/bulk/password-reset", {
      method: "POST",
      body: { ids: [first, second], send_by_email: true },
    });
    expect(state.result?.issued).toEqual([
      { id: first, login: "petrov", oneTimePassword: "p1", emailed: true, emailSkippedReason: undefined },
      {
        id: second,
        login: "blocked1",
        oneTimePassword: "p2",
        emailed: false,
        emailSkippedReason: "not_active",
      },
    ]);
    expect(revalidatePath).toHaveBeenCalledWith("/users", "layout");
  });

  test("passes the mail-off refusal through by its own code", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("mail_off", 409, "mail is off"));

    const state = await bulkResetPasswordAction({}, form({ ids: [first], send_by_email: "on" }));

    expect(state).toEqual({ code: "mail_off" });
    expect(revalidatePath).not.toHaveBeenCalled();
  });

  test("drops an id that is not one, and sends nothing when none is left", async () => {
    const state = await bulkResetPasswordAction({}, form({ ids: ["../roles"], send_by_email: "on" }));

    expect(state).toEqual({ code: "invalid_request" });
    expect(serverRequest).not.toHaveBeenCalled();
  });
});
