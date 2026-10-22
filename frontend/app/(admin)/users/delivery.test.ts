import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));

import { redirect } from "next/navigation";

import { ApiError } from "@/lib/api/client";

import { readMailOffer } from "./delivery";

let consoleError: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
  serverRequest.mockReset();
  consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
});

afterEach(() => {
  consoleError.mockRestore();
});

describe("readMailOffer", () => {
  // The accounts screens' own read, under users.manage: an account manager
  // without settings.manage is offered the option too.
  test("reads the mail status the accounts screens are allowed to read", async () => {
    serverRequest.mockResolvedValueOnce({ enabled: true, per_minute: 25 });

    expect(await readMailOffer()).toEqual({ status: "on", perMinute: 25 });
    expect(serverRequest).toHaveBeenCalledWith("/users/mail-status");
    expect(consoleError).not.toHaveBeenCalled();
  });

  test("reads mail as off only when the server says so", async () => {
    serverRequest.mockResolvedValueOnce({ enabled: false, per_minute: 0 });

    expect(await readMailOffer()).toEqual({ status: "off" });
  });

  test("reports a failed read as unknown, not as off, and logs it", async () => {
    const failure = new ApiError("internal_error", 500, "boom");
    serverRequest.mockRejectedValueOnce(failure);

    expect(await readMailOffer()).toEqual({ status: "unknown" });
    expect(consoleError).toHaveBeenCalledWith(
      "reading %s for the accounts screens failed",
      "/users/mail-status",
      failure,
    );
  });

  test("reports an answer it cannot read as unknown too", async () => {
    serverRequest.mockResolvedValueOnce({ enabled: "yes" });

    expect(await readMailOffer()).toEqual({ status: "unknown" });
    expect(consoleError).toHaveBeenCalledTimes(1);
  });

  test("lets Next's own control flow through rather than reading it as a failure", async () => {
    let control: unknown;
    try {
      redirect("/login");
    } catch (error) {
      control = error;
    }
    serverRequest.mockRejectedValueOnce(control);

    await expect(readMailOffer()).rejects.toBe(control);
    expect(consoleError).not.toHaveBeenCalled();
  });
});
