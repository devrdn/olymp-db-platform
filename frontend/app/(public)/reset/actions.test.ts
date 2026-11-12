import { beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";

const { serverRequest, redirect } = vi.hoisted(() => ({
  serverRequest: vi.fn(),
  redirect: vi.fn((target: string) => {
    throw new Error(`NEXT_REDIRECT ${target}`);
  }),
}));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("next/navigation", () => ({ redirect }));

import { resetPasswordAction } from "./actions";

const TOKEN = "Q".repeat(43);

function form(fields: Record<string, string>): FormData {
  const data = new FormData();
  for (const [key, value] of Object.entries(fields)) data.set(key, value);
  return data;
}

const filled = { token: TOKEN, password: "a new long secret", confirm: "a new long secret" };

beforeEach(() => {
  serverRequest.mockReset();
  redirect.mockClear();
});

describe("resetPasswordAction", () => {
  test("posts the link's token with the new password, untrimmed", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    await expect(
      resetPasswordAction({}, form({ ...filled, password: " spaced secret ", confirm: " spaced secret " })),
    ).rejects.toThrow(/NEXT_REDIRECT/);

    expect(serverRequest).toHaveBeenCalledWith("/public/password/reset", {
      method: "POST",
      body: { token: TOKEN, password: " spaced secret " },
    });
  });

  test("sends the person to sign in with the notice that the password changed", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    await expect(resetPasswordAction({}, form(filled))).rejects.toThrow(/NEXT_REDIRECT/);

    expect(redirect).toHaveBeenCalledWith("/login?changed=1");
  });

  test("refuses two different passwords without asking the API", async () => {
    expect(await resetPasswordAction({}, form({ ...filled, confirm: "different" }))).toEqual({
      code: "password_mismatch",
    });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("refuses what cannot be a link's token without asking the API", async () => {
    expect(await resetPasswordAction({}, form({ ...filled, token: "../../auth/me" }))).toEqual({
      code: "reset_link_invalid",
    });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  /** The state reaches the browser; the token must not travel back in it. */
  test("returns the API's code and never the token", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("reset_link_invalid", 410, "gone"));

    const state = await resetPasswordAction({}, form(filled));

    expect(state).toEqual({ code: "reset_link_invalid" });
    expect(JSON.stringify(state)).not.toContain(TOKEN);
  });

  test("reads the API having no such route as a link that cannot work", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("not_found", 404, "Resource not found"));

    expect((await resetPasswordAction({}, form(filled))).code).toBe("reset_link_invalid");
  });
});
