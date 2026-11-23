import { revalidatePath } from "next/cache";

import { apiOrigin } from "./config";
import { callerHeaders } from "./caller";
import { request, type RequestOptions } from "./client";
import { outcome, type Outcome } from "./outcome";
import { sessionHeader } from "@/lib/auth/session";

/**
 * How long a server-side call may wait for the API. Longer than the longest
 * answer the API itself waits for (a query queued for `QUERY_QUEUE_WAIT`,
 * 15 s, then run for its 5 s deadline), so a slow answer is never cut short;
 * without any bound, Node's fetch holds a request open for minutes, and a
 * stalled API holds every page's render with it.
 */
export const SERVER_REQUEST_TIMEOUT_MS = 30_000;

/**
 * Server-side access to the Core API: the transport plus where the API is and
 * who is asking, both the session (which account) and the forwarded chain
 * (which address; see `./forwarded`). A caller's own signal replaces the
 * deadline; a timed-out one surfaces as the `unreachable` ApiError.
 */
export async function serverRequest(path: string, options: RequestOptions = {}) {
  // The request first: a prerender stops on it, where `apiOrigin()` would
  // throw because a build has no API to name.
  const headers = { ...options.headers, ...(await callerHeaders()), ...(await sessionHeader()) };
  return request(path, {
    ...options,
    origin: apiOrigin(),
    headers,
    signal: options.signal ?? AbortSignal.timeout(SERVER_REQUEST_TIMEOUT_MS),
  });
}

/** A page to revalidate, or `{ layout }` for a layout and every page beneath it. */
export type Revalidation = string | { layout: string };

/** What changed, or a function of the answer when the answer decides (an import that added nobody). */
export type Revalidate<T> = readonly Revalidation[] | ((value: T) => readonly Revalidation[]);

/** A form bound to an action, after a call that worked. */
export type ActionDone = { ok: true; code?: undefined };

/** A form bound to an action, after a refusal: the code its sentence is found by. */
export type ActionRefused = { ok?: undefined; code: string };

/**
 * What a form bound to a server action holds: nothing yet, `ok`, or a
 * refusal's code, never both. An action's own extras sit beside these.
 */
export type ActionState = { ok?: undefined; code?: undefined } | ActionDone | ActionRefused;

export type ServerOutcomeOptions<T> = {
  revalidate?: Revalidate<T>;
  /** A best-effort read whose failure the page hides, named by its path for the log (see `outcome`). */
  report?: string;
};

/**
 * Runs one call on the server, from a page or an action, and revalidates
 * `revalidate` only once it succeeded. A failure is the outcome's, never a
 * throw; `redirect` and `notFound` still are.
 */
export async function serverOutcome<T>(
  call: () => Promise<T>,
  options: ServerOutcomeOptions<T> = {},
): Promise<Outcome<T>> {
  const { revalidate = [], report } = options;
  const result = await outcome(call, { report });
  if (result.ok) {
    for (const target of typeof revalidate === "function" ? revalidate(result.value) : revalidate) {
      if (typeof target === "string") revalidatePath(target);
      else revalidatePath(target.layout, "layout");
    }
  }
  return result;
}

/**
 * What an action's own `failure` may read of a refusal: the code, and the
 * subject a sentence names. The request id and the API's message stay on the
 * server; the game builder, which shows the message, reads the outcome itself.
 */
export type ActionFailure = { code: string; subject?: string };

type ActionOptions<T, S, F> = {
  revalidate?: Revalidate<T>;
  /**
   * The form's state once the call worked, `{ ok: true }` unless the form
   * needs what the call produced. Runs after revalidation, outside the
   * outcome, so an answer it cannot read is a contract break, not a refusal.
   */
  success?: (value: T) => S;
  /** The form's state after a refusal; only the code unless the action says what else crosses. */
  failure?: (failure: ActionFailure) => F;
};

/**
 * `serverOutcome` shaped into a form's state. Nothing of a failure reaches the
 * browser but its code, unless `failure` names more.
 */
export async function serverAction<T, S extends object = ActionDone, F extends object = ActionRefused>(
  call: () => Promise<T>,
  options: ActionOptions<T, S, F> = {},
): Promise<S | F> {
  const result = await serverOutcome(call, { revalidate: options.revalidate });
  if (result.ok) return options.success ? options.success(result.value) : ({ ok: true } as S);
  if (!options.failure) return { code: result.code } as unknown as F;

  // A fresh object, so a projection that returns its argument whole still
  // carries nothing but these two.
  const failure: ActionFailure = { code: result.code };
  if (result.subject !== undefined) failure.subject = result.subject;
  return options.failure(failure);
}
