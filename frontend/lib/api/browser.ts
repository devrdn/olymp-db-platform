import { request, requestDownload, type RequestOptions } from "./client";
import { outcome, type Failure } from "./outcome";

/**
 * Calls made from the browser straight to the API, same-origin through
 * `/api/v1`, with the session cookie. Used where a server action would wait
 * its turn: Next dispatches a page's actions one at a time, and the play
 * screen, the live monitor and the staff desk cannot queue behind a running
 * query.
 */

export type BrowserOptions = Omit<RequestOptions, "origin" | "credentials">;

/** `request` from the page's own origin; a failure is thrown as an `ApiError`. */
export function browserRequest(path: string, options: BrowserOptions = {}): Promise<unknown> {
  return request(path, { ...options, credentials: "same-origin" });
}

/** `requestDownload` from the page's own origin. */
export function browserDownload(path: string, options: BrowserOptions = {}) {
  return requestDownload(path, { ...options, credentials: "same-origin" });
}

/**
 * A browser call's answer when there is none to show. No screen these calls
 * feed shows the API's developer message, so it is left behind.
 */
export type Refused = { kind: "refused" } & Omit<Failure, "detail">;

/**
 * Runs a browser call into the state a screen renders: whatever the call
 * built from the answer, or `refused` with why. Never throws, but for a
 * caller's own abort.
 */
export async function browserCall<S>(call: () => Promise<S>): Promise<S | Refused> {
  const result = await outcome(call);
  if (result.ok) return result.value;

  const refused: Refused = { kind: "refused", code: result.code };
  if (result.requestId !== undefined) refused.requestId = result.requestId;
  if (result.subject !== undefined) refused.subject = result.subject;
  if (result.position !== undefined) refused.position = result.position;
  if (result.retryAfterSeconds !== undefined) refused.retryAfterSeconds = result.retryAfterSeconds;
  return refused;
}
