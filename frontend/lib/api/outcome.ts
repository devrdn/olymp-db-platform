import { unstable_rethrow } from "next/navigation";

import { ApiError, CLIENT_ERROR_CODES, isAbortError } from "./client";

/**
 * Why a call to the API produced no value, in the API's own terms. Only what
 * the API said is present: an absent key means it said nothing about it.
 */
export type Failure = {
  /** The API's code, or `unreachable` when the API itself never answered. */
  code: string;
  /** For the reference a participant can quote. */
  requestId?: string;
  /** What the code is about (which table, which function), when the API names it. */
  subject?: string;
  /** A 1-based offset into the query text, for a syntax error. */
  position?: number;
  /** A 429's `Retry-After`, in whole seconds. */
  retryAfterSeconds?: number;
  /**
   * The API's English message for developers. Never shown instead of the
   * dictionary's sentence; the game builder shows it beside one, because it
   * names the table or row at fault.
   */
  detail?: string;
};

export type Outcome<T> = { ok: true; value: T } | ({ ok: false } & Failure);

export type OutcomeOptions = {
  /**
   * A best-effort read whose failure the page hides, named by its path: every
   * failure of it is logged, a refusal the API gave included.
   */
  report?: string;
};

/**
 * Runs one call to the API and returns its value or why there is none, so no
 * caller writes its own catch. Anything the API did not answer in its own
 * terms (a gateway's page, a dead network, a deadline, an answer the schema
 * refuses) is `unreachable`. Next's `redirect`, `notFound` and prerender
 * bail-outs are rethrown, and so is a caller's own abort: neither is a failure
 * to show.
 */
export async function outcome<T>(call: () => Promise<T>, options: OutcomeOptions = {}): Promise<Outcome<T>> {
  try {
    return { ok: true, value: await call() };
  } catch (error: unknown) {
    unstable_rethrow(error);
    if (isAbortError(error)) throw error;
    // Schema drift, a bug or a dead network leaves no trace in the outcome, so
    // the server logs it. In a browser the console is the visitor's.
    if (options.report !== undefined || (!(error instanceof ApiError) && typeof window === "undefined")) {
      console.error(logLine(error, options.report));
    }
    return { ok: false, ...failureOf(error) };
  }
}

/**
 * One line that diagnoses a failure without repeating it. An error's message
 * is never logged: a rejected fetch quotes the header it refused, which can be
 * the sender's own cookie with a line break in it, and the API's message can
 * name its internals.
 */
function logLine(error: unknown, path: string | undefined): string {
  const parts = ["a call to the API failed"];
  if (path !== undefined) parts.push(`reading ${path}`);
  if (error instanceof ApiError) {
    parts.push(`with ${error.code} (${error.status})`);
    if (error.requestId) parts.push(`request ${error.requestId}`);
  } else {
    parts.push(`with ${error instanceof Error ? error.name : typeof error}`);
    const fields = driftedFields(error);
    if (fields) parts.push(`at ${fields}`);
  }
  return printable(parts.join(" "));
}

/**
 * Where a ZodError found the answer off the contract: field paths and issue
 * codes, never the values. Read by shape, so the browser bundle need not
 * import zod to recognise one.
 */
function driftedFields(error: unknown): string | undefined {
  const issues = (error as { issues?: unknown })?.issues;
  if (!Array.isArray(issues)) return undefined;
  return issues
    .slice(0, 5)
    .map((issue: { path?: unknown; code?: unknown }) => {
      const path = Array.isArray(issue?.path) ? issue.path.join(".") : "";
      return `${path || "(root)"}: ${String(issue?.code)}`;
    })
    .join(", ");
}

/** Strips control characters, so a value from a request cannot start a forged log line, and bounds the length. */
function printable(text: string): string {
  return text.replace(/[\u0000-\u001f\u007f-\u009f]/g, " ").slice(0, 500);
}

/**
 * The code alone, for a caller that catches a thrown `ApiError` itself (an
 * upload's abort, the autosave's refusal kinds): the API's code when it
 * answered, `unreachable` for anything else.
 */
export function failureCodeOf(error: unknown): string {
  return error instanceof ApiError ? error.code : CLIENT_ERROR_CODES.unreachable;
}

function failureOf(error: unknown): Failure {
  const code = failureCodeOf(error);
  // `client.ts` writes the API's private address into an unreachable error's
  // message, which must not travel on to a page.
  if (!(error instanceof ApiError) || code === CLIENT_ERROR_CODES.unreachable) return { code };

  const failure: Failure = { code };
  if (error.requestId !== undefined) failure.requestId = error.requestId;
  if (error.subject !== undefined) failure.subject = error.subject;
  if (error.position !== undefined) failure.position = error.position;
  if (error.retryAfterSeconds !== undefined) failure.retryAfterSeconds = error.retryAfterSeconds;
  if (error.message !== "") failure.detail = error.message;
  return failure;
}
