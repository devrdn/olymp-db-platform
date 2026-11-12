import { beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";

const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("@/lib/i18n/server", () => ({ activeLocale: async () => "ro" }));

import { forgotPasswordAction } from "./actions";

function form(login: string): FormData {
  const data = new FormData();
  data.set("login", login);
  return data;
}

beforeEach(() => {
  serverRequest.mockReset();
});

describe("forgotPasswordAction", () => {
  test("asks for a link in the reader's language, with what was typed trimmed", async () => {
    serverRequest.mockResolvedValueOnce({ status: "link_sent" });

    const state = await forgotPasswordAction({}, form("  ana@example.com "));

    expect(serverRequest).toHaveBeenCalledWith("/public/password/forgot?lang=ro", {
      method: "POST",
      body: { login: "ana@example.com" },
    });
    expect(state).toEqual({ sent: true });
  });

  /** The API answers alike for every identifier; so does the page. */
  test("says the same whatever was typed", async () => {
    serverRequest.mockResolvedValue({ status: "link_sent" });

    const known = await forgotPasswordAction({}, form("ana"));
    const unknown = await forgotPasswordAction({}, form("nobody-at-all"));

    expect(unknown).toEqual(known);
  });

  test("refuses an empty field without asking the API", async () => {
    expect(await forgotPasswordAction({}, form("   "))).toEqual({ code: "invalid_request", values: { login: "" } });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  /** Refused before anything was looked up, so it says nothing about the account. */
  test("passes on a refusal that came before any lookup, keeping what was typed", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("too_many_attempts", 429, "slow down"));

    expect(await forgotPasswordAction({}, form("ana"))).toEqual({
      code: "too_many_attempts",
      values: { login: "ana" },
    });
  });

  test("says recovery is not offered when the API has no such route", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("not_found", 404, "Resource not found"));

    expect(await forgotPasswordAction({}, form("ana"))).toEqual({ unavailable: true });
  });
});
