import { beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "./client";

const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("./server", () => ({ serverRequest }));

import { authMethodsSchema, fetchAuthMethods } from "./auth-methods";

const body = { password_signup: true, password_reset: false, microsoft: false, trusted_email_domain: "" };

beforeEach(() => {
  serverRequest.mockReset();
  vi.spyOn(console, "warn").mockImplementation(() => {});
});

describe("the ways in", () => {
  test("reads the API's answer", () => {
    expect(authMethodsSchema.parse({ ...body, trusted_email_domain: "example.md" })).toEqual({
      passwordSignup: true,
      passwordReset: false,
      microsoft: false,
      trustedEmailDomain: "example.md",
    });
  });

  test("are asked of the API", async () => {
    serverRequest.mockResolvedValueOnce(body);

    expect(await fetchAuthMethods()).toMatchObject({ passwordSignup: true, passwordReset: false });
    expect(serverRequest).toHaveBeenCalledWith("/auth/methods");
  });

  /** Sign-in must keep working when the API cannot say what else it offers. */
  test("are unknown, not an error, when the API cannot answer", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("unreachable", 502, "down"));

    expect(await fetchAuthMethods()).toBeNull();
  });

  test("are unknown when the answer is not the contract", async () => {
    serverRequest.mockResolvedValueOnce({ password_signup: "yes" });

    expect(await fetchAuthMethods()).toBeNull();
  });
});
