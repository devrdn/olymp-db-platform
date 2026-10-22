import { beforeEach, describe, expect, test, vi } from "vitest";

const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));

import { ApiError } from "@/lib/api/client";

import { readMailEnabled } from "./delivery";

beforeEach(() => {
  serverRequest.mockReset();
});

describe("readMailEnabled", () => {
  test("reads the mail status from the administrator's settings read", async () => {
    serverRequest.mockResolvedValueOnce({ values: {}, mail: { enabled: true, from: "olymp@usm.md" } });

    expect(await readMailEnabled()).toBe(true);
    expect(serverRequest).toHaveBeenCalledWith("/settings/all");
  });

  test("reads mail as off when the server says so or does not say", async () => {
    serverRequest.mockResolvedValueOnce({ values: {}, mail: { enabled: false, from: "" } });
    serverRequest.mockResolvedValueOnce({ values: {} });

    expect(await readMailEnabled()).toBe(false);
    expect(await readMailEnabled()).toBe(false);
  });

  // An account manager without settings.manage is refused this read; the
  // screen then offers no email option rather than failing.
  test("reads mail as off when the settings cannot be read", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("forbidden", 403, "forbidden"));
    serverRequest.mockRejectedValueOnce(new TypeError("fetch failed"));

    expect(await readMailEnabled()).toBe(false);
    expect(await readMailEnabled()).toBe(false);
  });
});
