import { redirect } from "next/navigation";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("./server", () => ({ serverRequest }));

import { branding, forgetBranding } from "./branding";

const named = (name: string) => ({ values: { "installation.name": name }, images: {} });

beforeEach(() => {
  forgetBranding();
  serverRequest.mockReset();
  vi.useFakeTimers();
  // A failed read is logged; the line itself is `outcome.test.ts`'s business.
  vi.spyOn(console, "error").mockImplementation(() => {});
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("branding", () => {
  test("asks the API once for the renders of the next half-minute", async () => {
    serverRequest.mockResolvedValue(named("USM"));

    await branding();
    vi.advanceTimersByTime(29_000);
    const later = await branding();

    expect(serverRequest).toHaveBeenCalledTimes(1);
    expect(later.name).toBe("USM");
  });

  test("asks again once the half-minute is over", async () => {
    serverRequest.mockResolvedValueOnce(named("USM")).mockResolvedValueOnce(named("USM Chișinău"));

    await branding();
    vi.advanceTimersByTime(30_000);
    const later = await branding();

    expect(serverRequest).toHaveBeenCalledTimes(2);
    expect(later.name).toBe("USM Chișinău");
  });

  test("asks again as soon as it is told the settings changed", async () => {
    serverRequest.mockResolvedValueOnce(named("USM")).mockResolvedValueOnce(named("USM Chișinău"));

    await branding();
    forgetBranding();
    const later = await branding();

    expect(later.name).toBe("USM Chișinău");
  });

  // The empty frame is a stand-in for an outage, not an answer to keep.
  test("does not keep a failed read", async () => {
    serverRequest.mockRejectedValueOnce(new TypeError("fetch failed")).mockResolvedValueOnce(named("USM"));

    expect((await branding()).name).toBe("");
    expect((await branding()).name).toBe("USM");
  });

  test("lets Next's own control flow through, as a prerender's bail-out on the request headers", async () => {
    serverRequest.mockImplementation(async () => redirect("/elsewhere"));

    await expect(branding()).rejects.toMatchObject({ digest: expect.stringContaining("NEXT_REDIRECT") });
  });

  test("reads the archive as off when the API cannot be reached", async () => {
    serverRequest.mockRejectedValueOnce(new TypeError("fetch failed"));

    expect((await branding()).archive).toBe(false);
  });
});
