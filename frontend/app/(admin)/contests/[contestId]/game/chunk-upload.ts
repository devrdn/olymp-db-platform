import { useEffect, useRef } from "react";

import { browserRequest } from "@/lib/api/browser";
import { ApiError, isAbortError } from "@/lib/api/client";

/**
 * A chunk refused as busy was not stored, so it is sent again after the
 * `Retry-After` the refusal names. The wait is held between the bounds, so a
 * missing or wild header can neither hammer the API nor stall the upload, and
 * a chunk refused `maxRefusals` times in a row is left to the organiser's Retry.
 */
const BUSY_RETRY = {
  maxRefusals: 5,
  minWaitSeconds: 1,
  maxWaitSeconds: 60,
  defaultWaitSeconds: 5,
} as const;

/**
 * Consecutive out-of-order refusals resynced against the server's count before
 * asking for Retry, so a broken upload does not spin forever.
 */
const MAX_AUTO_RESYNCS = 3;

/** What the dump and a table's data upload differently; the loop is the same. */
export type ChunkTarget = {
  /** The chunk endpoint under `/api/v1` for a byte offset. */
  chunkPath: (offset: number) => string;
  /** The refusal that stored nothing and is sent again after its wait. */
  busy: "game_upload_busy" | "game_table_data_busy";
  /** The refusal of a chunk sent at an offset the server is not at. */
  outOfOrder: "game_upload_chunk_out_of_order" | "game_table_data_chunk_out_of_order";
  /** The server's `received_bytes` now, or null once the upload is no longer receiving. */
  serverOffset: () => Promise<number | null>;
};

export type ChunkRun = {
  file: File;
  /** The offset to send from: the server's count, never this tab's tally. */
  from: number;
  chunkBytes: number;
  /** Called with the server's `received_bytes` after every chunk and resync. */
  onProgress: (receivedBytes: number) => void;
};

/** `stopped` is a cancel or a screen that went away, never a failure to show. */
export type ChunkLoopEnd = { kind: "sent" } | { kind: "stopped" } | { kind: "refused"; error: unknown };

/** The upload's `received_bytes` if it is still `id` and still receiving, else null. */
export function receivingOffset(
  upload: { id: string; status: string; receivedBytes: number } | null,
  id: string,
): number | null {
  return upload && upload.id === id && upload.status === "receiving" ? upload.receivedBytes : null;
}

/**
 * One upload attempt, from the organiser's pick, resume or Retry to its last
 * chunk. Cancel, a newer attempt or the screen going ends it; `send` then
 * sends nothing more.
 */
export type UploadAttempt = {
  /**
   * Whether the attempt goes on after one of the screen's awaits. False once it
   * has ended. When Cancel ended it, the upload its begin created, if any, is
   * ended on the server as Cancel ends any other; when the screen went, that
   * upload is left for a resume, as leaving leaves any other.
   */
  proceed: (createdUploadId?: string) => boolean;
  send: (target: ChunkTarget, run: ChunkRun) => Promise<ChunkLoopEnd>;
};

/** What Cancel needs from the screen asking it. */
export type CancelQuestion = {
  /** Asks the organiser; true to go on. */
  confirm: () => Promise<boolean>;
  /**
   * Read when Cancel is pressed, for the upload the question is about, and
   * again when the answer lands: the upload may have finished meanwhile, or
   * its begin may have answered with an id.
   */
  now: () => { cancellable: boolean; uploadId: string | null };
  /** Puts the panel back to its picker. */
  reset: () => void;
};

type Running = { controller: AbortController; cancelled: boolean };

/**
 * Sends a file's chunks for one screen and cancels them. `start()` opens an
 * attempt and ends the previous one; Cancel and the screen unmounting end the
 * current one, its request in flight and any wait included, so no chunk or
 * wait follows the screen's going or a cancel. A screen starts its attempt
 * before its first await, so both always find it. Only Cancel ends an upload
 * on the server (`abortOnServer`); a screen that goes leaves its upload
 * resumable.
 */
export function useChunkLoop(abortOnServer: (uploadId: string) => Promise<unknown>) {
  const currentRef = useRef<Running | null>(null);
  const abortRef = useRef(abortOnServer);
  useEffect(() => {
    abortRef.current = abortOnServer;
  });
  useEffect(() => () => currentRef.current?.controller.abort(), []);

  function start(): UploadAttempt {
    currentRef.current?.controller.abort();
    const running: Running = { controller: new AbortController(), cancelled: false };
    currentRef.current = running;
    const { signal } = running.controller;
    return {
      proceed: (createdUploadId) => {
        if (!signal.aborted) return true;
        if (running.cancelled && createdUploadId) void abortRef.current(createdUploadId);
        return false;
      },
      send: (target, run) => sendChunks(target, run, signal),
    };
  }

  /**
   * Cancels the upload Cancel was pressed for, once the organiser's yes lands:
   * it ends the attempt sending it by then, which a Retry or a resume of that
   * upload may have opened meanwhile, and ends on the server the upload the
   * screen holds by then (a begin may have answered while the question was
   * open). Nothing is done when the upload finished meanwhile, or when another
   * upload began while the question was coming up: that one was never asked
   * about.
   */
  async function cancel(question: CancelQuestion): Promise<void> {
    const askedAttempt = currentRef.current;
    const askedUpload = question.now().uploadId;
    if (!(await question.confirm().catch(() => false))) return;
    const { cancellable, uploadId } = question.now();
    const sameAttempt = currentRef.current === askedAttempt;
    const sameUpload = askedUpload !== null && uploadId === askedUpload;
    if (!(sameAttempt || sameUpload) || !cancellable) return;
    const running = currentRef.current;
    if (running) {
      running.cancelled = true;
      running.controller.abort();
    }
    question.reset();
    if (uploadId) await abortRef.current(uploadId);
  }

  return { start, cancel };
}

/**
 * Sends the chunks from `run.from` to the end of the file, one at a time
 * (parallel PUTs would race the same offset), each from the offset the server
 * last answered. Any refusal but a busy or an out-of-order one is handed back,
 * and so is a failure to read the server's count.
 */
export async function sendChunks(target: ChunkTarget, run: ChunkRun, signal: AbortSignal): Promise<ChunkLoopEnd> {
  let offset = run.from;
  let resyncs = 0;

  while (offset < run.file.size) {
    if (signal.aborted) return { kind: "stopped" };
    const chunk = run.file.slice(offset, Math.min(offset + run.chunkBytes, run.file.size));
    try {
      offset = await putChunk(target, offset, chunk, run.file.size, signal);
      resyncs = 0;
      run.onProgress(offset);
    } catch (error) {
      if (isAbortError(error) || signal.aborted) return { kind: "stopped" };

      // The server knows the true offset better than this tab, mid-stream as
      // much as on resume. A count naming the offset just refused, or none
      // inside the file, would only repeat the refusal.
      if (error instanceof ApiError && error.code === target.outOfOrder && resyncs < MAX_AUTO_RESYNCS) {
        resyncs += 1;
        let fresh: number | null;
        try {
          fresh = await target.serverOffset();
        } catch (unread) {
          return signal.aborted ? { kind: "stopped" } : { kind: "refused", error: unread };
        }
        if (signal.aborted) return { kind: "stopped" };
        if (fresh !== null && fresh !== offset && isOffsetIn(fresh, 0, run.file.size)) {
          offset = fresh;
          run.onProgress(offset);
          continue;
        }
      }
      return { kind: "refused", error };
    }
  }

  // A cancel that lands with the last chunk must not let the screen complete.
  return signal.aborted ? { kind: "stopped" } : { kind: "sent" };
}

/**
 * Sends one chunk straight to the API, not through a Server Action: an
 * action's body is capped at 1 MiB while `chunk_bytes` is configured per
 * installation, and `rawBody: Blob` streams the slice from disk where an
 * action buffers its `FormData`. It is a same-origin `/api` call, forwarded by
 * the reverse proxy or, without one, the `next.config.ts` rewrite. Retries a
 * busy refusal; throws any other one, and the last busy one. A count that does
 * not move past `offset` within the file is thrown as malformed: it would
 * slice from the wrong place or resend at once with no wait.
 */
async function putChunk(
  target: ChunkTarget,
  offset: number,
  chunk: Blob,
  fileSize: number,
  signal: AbortSignal,
): Promise<number> {
  for (let refusals = 1; ; refusals += 1) {
    try {
      const payload = await browserRequest(target.chunkPath(offset), { method: "PUT", rawBody: chunk, signal });
      const bytes = (payload as { received_bytes?: unknown } | null)?.received_bytes;
      if (!isOffsetIn(bytes, offset + 1, fileSize)) throw new Error(`malformed chunk response at offset ${offset}`);
      return bytes;
    } catch (error) {
      if (!(error instanceof ApiError) || error.code !== target.busy || refusals >= BUSY_RETRY.maxRefusals) throw error;
      await waitOut(busyWaitMs(error.retryAfterSeconds), signal);
    }
  }
}

/** Whether `value` is a whole byte count from `min` to `max`, both included. */
function isOffsetIn(value: unknown, min: number, max: number): value is number {
  return Number.isSafeInteger(value) && (value as number) >= min && (value as number) <= max;
}

function busyWaitMs(retryAfterSeconds: number | undefined): number {
  const seconds = retryAfterSeconds ?? BUSY_RETRY.defaultWaitSeconds;
  return Math.min(Math.max(seconds, BUSY_RETRY.minWaitSeconds), BUSY_RETRY.maxWaitSeconds) * 1000;
}

/** Resolves after `ms`, or rejects as an abort the moment `signal` fires, clearing its timer. */
function waitOut(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const aborted = () => new DOMException("The upload was cancelled.", "AbortError");
    if (signal.aborted) {
      reject(aborted());
      return;
    }
    const onAbort = () => {
      clearTimeout(timer);
      reject(aborted());
    };
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    signal.addEventListener("abort", onAbort, { once: true });
  });
}
