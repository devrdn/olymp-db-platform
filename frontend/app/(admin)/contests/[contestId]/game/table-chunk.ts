import { browserRequest } from "@/lib/api/browser";
import { ApiError } from "@/lib/api/client";

/**
 * A chunk the API refuses as `game_table_data_busy` was not stored, so it is
 * sent again after the `Retry-After` the refusal names. The wait is held
 * between the bounds, so a missing or wild header can neither hammer the API
 * nor stall the upload, and a chunk refused `maxRefusals` times in a row is
 * left to the organiser's Retry.
 */
const BUSY_RETRY = {
  maxRefusals: 5,
  minWaitSeconds: 1,
  maxWaitSeconds: 60,
  defaultWaitSeconds: 5,
} as const;

const BUSY = "game_table_data_busy";

/**
 * Sends one chunk straight to the API and returns the server's
 * `received_bytes`: a Server Action's body limit is far below the configured
 * chunk size, and `rawBody: Blob` streams the slice from disk instead of
 * buffering it as `FormData`. A busy refusal is retried here; any other one,
 * and the last busy one, is thrown. Aborting `signal` ends a wait at once.
 */
export async function putTableChunk(
  contestId: string,
  table: string,
  id: string,
  offset: number,
  chunk: Blob,
  signal: AbortSignal,
): Promise<number> {
  for (let refusals = 1; ; refusals += 1) {
    try {
      return await sendChunk(contestId, table, id, offset, chunk, signal);
    } catch (error) {
      if (!(error instanceof ApiError) || error.code !== BUSY || refusals >= BUSY_RETRY.maxRefusals) throw error;
      await waitOut(busyWaitMs(error.retryAfterSeconds), signal);
    }
  }
}

async function sendChunk(
  contestId: string,
  table: string,
  id: string,
  offset: number,
  chunk: Blob,
  signal: AbortSignal,
): Promise<number> {
  const payload = await browserRequest(
    `/contests/${contestId}/game/tables/${encodeURIComponent(table)}/data/${id}/chunk?offset=${offset}`,
    { method: "PUT", rawBody: chunk, signal },
  );

  const bytes = (payload as { received_bytes?: unknown } | null)?.received_bytes;
  if (typeof bytes !== "number") {
    throw new Error(`malformed chunk response for table upload ${id}`);
  }
  return bytes;
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
