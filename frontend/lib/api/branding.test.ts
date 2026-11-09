import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("./server", () => ({ serverRequest }));

import { branding, forgetBranding } from "./branding";

const named = (name: string) => ({ values: { "installation.name": name }, images: {} });

beforeEach(() => {
  forgetBranding();
  serverRequest.mockReset();
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
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
});
