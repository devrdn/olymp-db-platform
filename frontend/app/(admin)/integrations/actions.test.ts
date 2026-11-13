import { beforeEach, describe, expect, test, vi } from "vitest";

const { revalidatePath, serverRequest } = vi.hoisted(() => ({
  revalidatePath: vi.fn(),
  serverRequest: vi.fn(),
}));
vi.mock("next/cache", () => ({ revalidatePath }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));

import { ApiError } from "@/lib/api/client";

import { switchProviderAction } from "./actions";

function form(fields: Record<string, string>): FormData {
  const data = new FormData();
  for (const [key, value] of Object.entries(fields)) data.set(key, value);
  return data;
}

beforeEach(() => {
  serverRequest.mockReset();
  revalidatePath.mockReset();
});

describe("switchProviderAction", () => {
  test("refuses a provider this release does not know, without a request", async () => {
    const state = await switchProviderAction({}, form({ provider: "okta", enabled: "false" }));

    expect(state).toEqual({ code: "invalid_request" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("refuses an enabled value other than true or false, without a request", async () => {
    const state = await switchProviderAction({}, form({ provider: "google", enabled: "maybe" }));

    expect(state).toEqual({ code: "invalid_request" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("refuses a missing provider", async () => {
    expect(await switchProviderAction({}, form({ enabled: "true" }))).toEqual({ code: "invalid_request" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("switches a provider off and refreshes the page", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    const state = await switchProviderAction({}, form({ provider: "google", enabled: "false" }));

    expect(serverRequest).toHaveBeenCalledWith("/integrations/google", {
      method: "PUT",
      body: { enabled: false },
    });
    expect(revalidatePath).toHaveBeenCalledWith("/integrations");
    expect(state).toEqual({ saved: true });
  });

  test("switches a provider on", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    await switchProviderAction({}, form({ provider: "github", enabled: "true" }));

    expect(serverRequest).toHaveBeenCalledWith("/integrations/github", { method: "PUT", body: { enabled: true } });
  });

  test("passes the API's refusal through by its code, without refreshing", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("not_found", 404, "no"));

    const state = await switchProviderAction({}, form({ provider: "google", enabled: "true" }));

    expect(state).toEqual({ code: "not_found" });
    expect(revalidatePath).not.toHaveBeenCalled();
  });
});
