import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError, isAbortError } from "@/lib/api/client";

const browserRequest = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api/browser", () => ({ browserRequest }));

import { putTableChunk } from "./table-chunk";

const contestId = "11111111-1111-1111-1111-111111111111";
const dataId = "33333333-3333-3333-3333-333333333333";
const chunkPath = `/contests/${contestId}/game/tables/suspects/data/${dataId}/chunk?offset=5`;

function busy(retryAfterSeconds?: number) {
  return new ApiError("game_table_data_busy", 503, "busy", undefined, undefined, undefined, retryAfterSeconds);
}

function put(signal = new AbortController().signal) {
  return putTableChunk(contestId, "suspects", dataId, 5, new Blob(["abcde"]), signal);
}

/** Settles `promise` into a value or an error, so a rejection is never left unhandled while timers run. */
function settle<T>(promise: Promise<T>) {
  const state: { value?: T; error?: unknown; done: boolean } = { done: false };
  promise.then(
    (value) => Object.assign(state, { value, done: true }),
    (error: unknown) => Object.assign(state, { error, done: true }),
  );
  return state;
}

beforeEach(() => {
  vi.useFakeTimers();
  browserRequest.mockReset();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("a table-data chunk refused as busy", () => {
  test("is sent again, from the same offset, once the API's Retry-After has passed", async () => {
    browserRequest.mockRejectedValueOnce(busy(3)).mockResolvedValueOnce({ received_bytes: 10 });

    const sent = settle(put());
    await vi.advanceTimersByTimeAsync(2999);
    expect(browserRequest).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(1);
    expect(sent).toEqual({ value: 10, done: true });
    expect(browserRequest).toHaveBeenCalledTimes(2);
    expect(browserRequest).toHaveBeenLastCalledWith(chunkPath, expect.objectContaining({ method: "PUT" }));
  });

  test("is given up after five refusals in a row, with the last refusal", async () => {
    browserRequest.mockRejectedValue(busy(2));

    const sent = settle(put());
    await vi.advanceTimersByTimeAsync(60_000);

    expect(browserRequest).toHaveBeenCalledTimes(5);
    expect(sent.done).toBe(true);
    expect(sent.error).toMatchObject({ code: "game_table_data_busy" });
  });

  test("waits five seconds when the API names no wait", async () => {
    browserRequest.mockRejectedValueOnce(busy()).mockResolvedValueOnce({ received_bytes: 10 });

    const sent = settle(put());
    await vi.advanceTimersByTimeAsync(4999);
    expect(sent.done).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    expect(sent.value).toBe(10);
  });

  test("waits at least a second, so a zero wait cannot hammer the API", async () => {
    browserRequest.mockRejectedValueOnce(busy(0)).mockResolvedValueOnce({ received_bytes: 10 });

    const sent = settle(put());
    await vi.advanceTimersByTimeAsync(999);
    expect(sent.done).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    expect(sent.value).toBe(10);
  });

  test("waits a minute at most, so a wild wait cannot stall the upload", async () => {
    browserRequest.mockRejectedValueOnce(busy(86_400)).mockResolvedValueOnce({ received_bytes: 10 });

    const sent = settle(put());
    await vi.advanceTimersByTimeAsync(59_999);
    expect(sent.done).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    expect(sent.value).toBe(10);
  });

  test("stops waiting the moment the upload is cancelled, and leaves no timer behind", async () => {
    browserRequest.mockRejectedValueOnce(busy(30));
    const controller = new AbortController();

    const sent = settle(put(controller.signal));
    await vi.advanceTimersByTimeAsync(0);
    expect(vi.getTimerCount()).toBe(1);

    controller.abort();
    await vi.advanceTimersByTimeAsync(0);

    expect(sent.done).toBe(true);
    expect(isAbortError(sent.error)).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(browserRequest).toHaveBeenCalledTimes(1);
  });

  test("sends nothing more for an upload cancelled while its chunk was being refused", async () => {
    const controller = new AbortController();
    browserRequest.mockImplementationOnce(async () => {
      controller.abort();
      throw busy(2);
    });

    const sent = settle(put(controller.signal));
    await vi.advanceTimersByTimeAsync(60_000);

    expect(isAbortError(sent.error)).toBe(true);
    expect(browserRequest).toHaveBeenCalledTimes(1);
  });

  test("any other refusal is not retried", async () => {
    browserRequest.mockRejectedValueOnce(
      new ApiError("game_table_data_chunk_too_large", 400, "too big", undefined, undefined, undefined, 2),
    );

    const sent = settle(put());
    await vi.advanceTimersByTimeAsync(60_000);

    expect(sent.error).toMatchObject({ code: "game_table_data_chunk_too_large" });
    expect(browserRequest).toHaveBeenCalledTimes(1);
  });
});
