import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";

const browserRequest = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api/browser", () => ({ browserRequest }));

import { receivingOffset, sendChunks, useChunkLoop, type ChunkLoopEnd, type ChunkTarget } from "./chunk-upload";

const id = "33333333-3333-3333-3333-333333333333";

const serverOffset = vi.fn<() => Promise<number | null>>();
const target: ChunkTarget = {
  chunkPath: (offset) => `/uploads/${id}/chunk?offset=${offset}`,
  busy: "game_table_data_busy",
  outOfOrder: "game_table_data_chunk_out_of_order",
  serverOffset,
};

const onProgress = vi.fn();

function busy(retryAfterSeconds?: number) {
  return new ApiError("game_table_data_busy", 503, "busy", undefined, undefined, undefined, retryAfterSeconds);
}

/** The server's count after each chunk: whatever the chunk carried, added on. */
function serverCounts() {
  let received = 0;
  return async (_path: string, options: { rawBody?: Blob }) => {
    received += options.rawBody?.size ?? 0;
    return { received_bytes: received };
  };
}

/** Sends a 10-byte file from 0 in 5-byte chunks; the end lands in `state` once known. */
function send(signal = new AbortController().signal) {
  const state: { end?: ChunkLoopEnd } = {};
  void sendChunks(target, { file: new File(["0123456789"], "rows.csv"), from: 0, chunkBytes: 5, onProgress }, signal).then(
    (end) => (state.end = end),
  );
  return state;
}

beforeEach(() => {
  vi.useFakeTimers();
  browserRequest.mockReset();
  serverOffset.mockReset();
  onProgress.mockReset();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("sending a file's chunks", () => {
  test("sends each chunk from the offset the server last answered, and reports it", async () => {
    browserRequest.mockResolvedValueOnce({ received_bytes: 7 }).mockResolvedValueOnce({ received_bytes: 10 });

    const sent = send();
    await vi.advanceTimersByTimeAsync(0);

    expect(sent.end).toEqual({ kind: "sent" });
    expect(browserRequest.mock.calls.map((call) => call[0])).toEqual([
      `/uploads/${id}/chunk?offset=0`,
      `/uploads/${id}/chunk?offset=7`,
    ]);
    expect(onProgress.mock.calls).toEqual([[7], [10]]);
  });

  test("picks up from the server's own count after an out-of-order refusal", async () => {
    browserRequest
      .mockRejectedValueOnce(new ApiError("game_table_data_chunk_out_of_order", 409, "offset"))
      .mockResolvedValueOnce({ received_bytes: 10 });
    serverOffset.mockResolvedValueOnce(5);

    const sent = send();
    await vi.advanceTimersByTimeAsync(0);

    expect(sent.end).toEqual({ kind: "sent" });
    expect(browserRequest).toHaveBeenLastCalledWith(`/uploads/${id}/chunk?offset=5`, expect.anything());
  });

  test("hands back an out-of-order refusal once the upload is no longer receiving", async () => {
    const refusal = new ApiError("game_table_data_chunk_out_of_order", 409, "offset");
    browserRequest.mockRejectedValueOnce(refusal);
    serverOffset.mockResolvedValueOnce(null);

    const sent = send();
    await vi.advanceTimersByTimeAsync(0);

    expect(sent.end).toEqual({ kind: "refused", error: refusal });
  });

  test("hands any other refusal back at once, Retry-After or not", async () => {
    const refusal = new ApiError("game_table_data_chunk_too_large", 400, "too big", undefined, undefined, undefined, 2);
    browserRequest.mockRejectedValueOnce(refusal);

    const sent = send();
    await vi.advanceTimersByTimeAsync(60_000);

    expect(sent.end).toEqual({ kind: "refused", error: refusal });
    expect(browserRequest).toHaveBeenCalledTimes(1);
  });

  test("hands back an answer with no received_bytes as a refusal", async () => {
    browserRequest.mockResolvedValueOnce({});

    const sent = send();
    await vi.advanceTimersByTimeAsync(0);

    expect(sent.end?.kind).toBe("refused");
  });

  // A count that is not a whole number past the offset sent and within the
  // file would slice from the wrong place, or resend at once with no wait.
  test.each([
    ["a fraction", 2.5],
    ["a negative count", -1],
    ["the offset just sent", 0],
    ["more than the file holds", 11],
    ["a string", "5"],
  ])("hands back %s as received_bytes as a refusal, sending nothing more", async (_name, bytes) => {
    browserRequest.mockResolvedValueOnce({ received_bytes: bytes });

    const sent = send();
    await vi.advanceTimersByTimeAsync(60_000);

    expect(sent.end?.kind).toBe("refused");
    expect(browserRequest).toHaveBeenCalledTimes(1);
  });
});

describe("an out-of-order refusal", () => {
  const outOfOrder = () => new ApiError("game_table_data_chunk_out_of_order", 409, "offset");

  test("is handed back at once when the server names the offset just refused", async () => {
    const refusal = outOfOrder();
    browserRequest.mockRejectedValueOnce(refusal);
    serverOffset.mockResolvedValue(0);

    const sent = send();
    await vi.advanceTimersByTimeAsync(60_000);

    expect(sent.end).toEqual({ kind: "refused", error: refusal });
    expect(browserRequest).toHaveBeenCalledTimes(1);
    expect(serverOffset).toHaveBeenCalledTimes(1);
  });

  test.each([
    ["a negative count", -1],
    ["more than the file holds", 11],
    ["a fraction", 2.5],
  ])("is handed back when the server's count is %s", async (_name, count) => {
    const refusal = outOfOrder();
    browserRequest.mockRejectedValueOnce(refusal);
    serverOffset.mockResolvedValue(count);

    const sent = send();
    await vi.advanceTimersByTimeAsync(60_000);

    expect(sent.end).toEqual({ kind: "refused", error: refusal });
    expect(browserRequest).toHaveBeenCalledTimes(1);
  });

  test("counts every resync that stores nothing, so a server that keeps moving cannot hold the loop", async () => {
    browserRequest.mockResolvedValueOnce({ received_bytes: 5 }).mockRejectedValue(outOfOrder());
    serverOffset.mockResolvedValueOnce(3).mockResolvedValueOnce(4).mockResolvedValueOnce(3).mockResolvedValue(4);

    const sent = send();
    await vi.advanceTimersByTimeAsync(60_000);

    expect(sent.end?.kind).toBe("refused");
    expect(serverOffset).toHaveBeenCalledTimes(3);
    expect(browserRequest.mock.calls.map((call) => call[0])).toEqual([
      `/uploads/${id}/chunk?offset=0`,
      `/uploads/${id}/chunk?offset=5`,
      `/uploads/${id}/chunk?offset=3`,
      `/uploads/${id}/chunk?offset=4`,
      `/uploads/${id}/chunk?offset=3`,
    ]);
  });

  test("ends as stopped when the upload is cancelled while the server's count is read", async () => {
    const controller = new AbortController();
    browserRequest.mockRejectedValueOnce(outOfOrder());
    serverOffset.mockImplementationOnce(async () => {
      controller.abort();
      return null;
    });

    const sent = send(controller.signal);
    await vi.advanceTimersByTimeAsync(0);

    expect(sent.end).toEqual({ kind: "stopped" });
  });

  test("hands back the failure when the server's count cannot be read, so the screen offers Retry", async () => {
    const unreachable = new TypeError("fetch failed");
    browserRequest.mockRejectedValueOnce(outOfOrder());
    serverOffset.mockRejectedValueOnce(unreachable);

    const sent = send();
    await vi.advanceTimersByTimeAsync(0);

    expect(sent.end).toEqual({ kind: "refused", error: unreachable });
  });

  test("ends as stopped, without reading the server's count, when the cancel came with the refusal", async () => {
    const controller = new AbortController();
    browserRequest.mockImplementationOnce(async () => {
      controller.abort();
      throw outOfOrder();
    });

    const sent = send(controller.signal);
    await vi.advanceTimersByTimeAsync(0);

    expect(sent.end).toEqual({ kind: "stopped" });
    expect(serverOffset).not.toHaveBeenCalled();
  });
});

describe("a chunk refused as busy", () => {
  test("is sent again, from the same offset, once the API's Retry-After has passed", async () => {
    browserRequest.mockRejectedValueOnce(busy(3)).mockImplementation(serverCounts());

    const sent = send();
    await vi.advanceTimersByTimeAsync(2999);
    expect(browserRequest).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(1);
    expect(sent.end).toEqual({ kind: "sent" });
    expect(browserRequest.mock.calls.map((call) => call[0])).toEqual([
      `/uploads/${id}/chunk?offset=0`,
      `/uploads/${id}/chunk?offset=0`,
      `/uploads/${id}/chunk?offset=5`,
    ]);
  });

  test("is given up after five refusals in a row, with the last refusal", async () => {
    browserRequest.mockRejectedValue(busy(2));

    const sent = send();
    await vi.advanceTimersByTimeAsync(60_000);

    expect(browserRequest).toHaveBeenCalledTimes(5);
    expect(sent.end).toMatchObject({ kind: "refused", error: { code: "game_table_data_busy" } });
  });

  test("counts its refusals per chunk, so a busy spell on one chunk does not shorten the next one's", async () => {
    const answers = serverCounts();
    browserRequest
      .mockRejectedValueOnce(busy(1))
      .mockRejectedValueOnce(busy(1))
      .mockRejectedValueOnce(busy(1))
      .mockRejectedValueOnce(busy(1))
      .mockImplementationOnce(answers)
      .mockRejectedValueOnce(busy(1))
      .mockRejectedValueOnce(busy(1))
      .mockRejectedValueOnce(busy(1))
      .mockRejectedValueOnce(busy(1))
      .mockImplementationOnce(answers);

    const sent = send();
    await vi.advanceTimersByTimeAsync(60_000);

    expect(sent.end).toEqual({ kind: "sent" });
  });

  test("is not retried when the target treats another refusal as busy", async () => {
    browserRequest.mockRejectedValueOnce(
      new ApiError("game_upload_busy", 503, "busy", undefined, undefined, undefined, 2),
    );

    const sent = send();
    await vi.advanceTimersByTimeAsync(60_000);

    expect(sent.end).toMatchObject({ kind: "refused", error: { code: "game_upload_busy" } });
    expect(browserRequest).toHaveBeenCalledTimes(1);
  });

  test("waits five seconds when the API names no wait", async () => {
    browserRequest.mockRejectedValueOnce(busy()).mockImplementation(serverCounts());

    const sent = send();
    await vi.advanceTimersByTimeAsync(4999);
    expect(sent.end).toBeUndefined();
    await vi.advanceTimersByTimeAsync(1);
    expect(sent.end).toEqual({ kind: "sent" });
  });

  test("waits at least a second, so a zero wait cannot hammer the API", async () => {
    browserRequest.mockRejectedValueOnce(busy(0)).mockImplementation(serverCounts());

    const sent = send();
    await vi.advanceTimersByTimeAsync(999);
    expect(sent.end).toBeUndefined();
    await vi.advanceTimersByTimeAsync(1);
    expect(sent.end).toEqual({ kind: "sent" });
  });

  test("waits a minute at most, so a wild wait cannot stall the upload", async () => {
    browserRequest.mockRejectedValueOnce(busy(86_400)).mockImplementation(serverCounts());

    const sent = send();
    await vi.advanceTimersByTimeAsync(59_999);
    expect(sent.end).toBeUndefined();
    await vi.advanceTimersByTimeAsync(1);
    expect(sent.end).toEqual({ kind: "sent" });
  });

  test("stops waiting the moment the upload is cancelled, and leaves no timer behind", async () => {
    browserRequest.mockRejectedValueOnce(busy(30));
    const controller = new AbortController();

    const sent = send(controller.signal);
    await vi.advanceTimersByTimeAsync(0);
    expect(vi.getTimerCount()).toBe(1);

    controller.abort();
    await vi.advanceTimersByTimeAsync(0);

    expect(sent.end).toEqual({ kind: "stopped" });
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

    const sent = send(controller.signal);
    await vi.advanceTimersByTimeAsync(60_000);

    expect(sent.end).toEqual({ kind: "stopped" });
    expect(browserRequest).toHaveBeenCalledTimes(1);
  });
});

describe("an upload cancelled before its first chunk", () => {
  test("sends nothing and ends as stopped", async () => {
    const controller = new AbortController();
    controller.abort();

    const sent = send(controller.signal);
    await vi.advanceTimersByTimeAsync(0);

    expect(sent.end).toEqual({ kind: "stopped" });
    expect(browserRequest).not.toHaveBeenCalled();
  });
});

describe("a cancel that lands with the last chunk", () => {
  test("stops the run, so the screen does not complete a cancelled upload", async () => {
    const controller = new AbortController();
    browserRequest.mockResolvedValueOnce({ received_bytes: 5 }).mockImplementationOnce(async () => {
      controller.abort();
      return { received_bytes: 10 };
    });

    const sent = send(controller.signal);
    await vi.advanceTimersByTimeAsync(0);

    expect(sent.end).toEqual({ kind: "stopped" });
  });
});

describe("the server's count for an upload", () => {
  test("is its received_bytes while it is the same upload and still receiving", () => {
    expect(receivingOffset({ id, status: "receiving", receivedBytes: 7 }, id)).toBe(7);
  });

  test("is unknown for another upload, a finished one, or none", () => {
    expect(receivingOffset({ id: "other", status: "receiving", receivedBytes: 7 }, id)).toBeNull();
    expect(receivingOffset({ id, status: "complete", receivedBytes: 7 }, id)).toBeNull();
    expect(receivingOffset(null, id)).toBeNull();
  });
});

describe("a Cancel asked before the upload had an id", () => {
  test("cancels nothing once a newer attempt began while the question was open", async () => {
    const abortOnServer = vi.fn(async () => ({}));
    const reset = vi.fn();
    const { result } = renderHook(() => useChunkLoop(abortOnServer));
    let answer: (yes: boolean) => void = () => {};

    act(() => void result.current.start());
    let cancelled: Promise<void> = Promise.resolve();
    act(() => {
      cancelled = result.current.cancel({
        confirm: () => new Promise<boolean>((resolve) => (answer = resolve)),
        now: () => ({ cancellable: true, uploadId: null }),
        reset,
        refused: vi.fn(),
      });
    });
    let newer: ReturnType<typeof result.current.start> | undefined;
    act(() => void (newer = result.current.start()));
    await act(async () => {
      answer(true);
      await cancelled;
    });

    expect(reset).not.toHaveBeenCalled();
    expect(abortOnServer).not.toHaveBeenCalled();
    expect(newer?.proceed()).toBe(true);
  });
});

describe("a Cancel the server refuses", () => {
  function question(refused: (code: string) => void) {
    return { confirm: async () => true, now: () => ({ cancellable: true, uploadId: id }), reset: vi.fn(), refused };
  }

  test("says why, by the refusal's code, once the panel is back at its picker", async () => {
    const refused = vi.fn();
    const { result } = renderHook(() => useChunkLoop(async () => ({ code: "game_upload_already_complete" })));
    act(() => void result.current.start());

    await act(() => result.current.cancel(question(refused)));

    expect(refused).toHaveBeenCalledWith("game_upload_already_complete");
  });

  test("says the server could not be reached when the abort itself fails", async () => {
    const refused = vi.fn();
    const { result } = renderHook(() =>
      useChunkLoop(async () => {
        throw new TypeError("fetch failed");
      }),
    );
    act(() => void result.current.start());

    await act(() => result.current.cancel(question(refused)));

    expect(refused).toHaveBeenCalledWith("unreachable");
  });

  test("says nothing over an upload the organiser began while the abort was answered", async () => {
    const refused = vi.fn();
    let answer: (state: { code?: string }) => void = () => {};
    const { result } = renderHook(() => useChunkLoop(() => new Promise((resolve) => (answer = resolve))));
    act(() => void result.current.start());

    let cancelled: Promise<void> = Promise.resolve();
    await act(async () => {
      cancelled = result.current.cancel(question(refused));
    });
    act(() => void result.current.start());
    await act(async () => {
      answer({ code: "game_upload_already_complete" });
      await cancelled;
    });

    expect(refused).not.toHaveBeenCalled();
  });

  test("says nothing over a newer upload when a cancelled begin's late abort is refused", async () => {
    const refused = vi.fn();
    const { result } = renderHook(() => useChunkLoop(async () => ({ code: "unreachable" })));
    let cancelledBegin: ReturnType<typeof result.current.start> | undefined;
    act(() => void (cancelledBegin = result.current.start()));

    await act(() =>
      result.current.cancel({ ...question(refused), now: () => ({ cancellable: true, uploadId: null }) }),
    );
    act(() => void result.current.start());
    await act(async () => void cancelledBegin?.proceed(id));

    expect(refused).not.toHaveBeenCalled();
  });

  test("says why when it ends an upload whose begin answered after the yes", async () => {
    const refused = vi.fn();
    const { result } = renderHook(() => useChunkLoop(async () => ({ code: "unreachable" })));
    let attempt: ReturnType<typeof result.current.start> | undefined;
    act(() => void (attempt = result.current.start()));

    await act(() =>
      result.current.cancel({ ...question(refused), now: () => ({ cancellable: true, uploadId: null }) }),
    );
    await act(async () => void attempt?.proceed(id));

    expect(refused).toHaveBeenCalledWith("unreachable");
  });
});
