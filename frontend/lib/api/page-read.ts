import { notFound, redirect } from "next/navigation";
import { cache } from "react";

import { authRecoveryRedirect } from "@/lib/auth/guard";

import { ApiError } from "./client";
import { bestEffort } from "./outcome";
import { serverRequest } from "./server";

/**
 * How one page answers its read. Every read keeps three rules whatever the
 * policy: a dead session goes to sign-in and a one-time password to the
 * password screen, since a retry fixes neither; the code `not_found`, never a
 * status, is the not-found page; Next's own redirects, not-found and prerender
 * bail-outs always pass. Anything the policy does not name is thrown to the
 * error boundary.
 */
export type ReadPolicy = {
  /** The page's address, query included, that sign-in leads back to. */
  here: string;
  /** Codes besides `not_found` that mean the page's own subject is not there. */
  missing?: readonly string[];
  /** Answers `forbidden` as not found, where whether the thing exists is not the caller's business. */
  hideForbidden?: boolean;
  /** Codes that mean an ordinary absence the page renders: answered as null, and never logged. */
  absent?: readonly string[];
  /** Codes whose sentence the page shows in place of what it read, a rate limit: answered as a `Refusal`. */
  shown?: readonly string[];
  /**
   * The page renders without this read, and nothing becomes the not-found
   * page. A code that would have been is null without a log line, since
   * anyone can ask for an address that leads nowhere; any other failure is
   * null and logged with the path.
   */
  bestEffort?: boolean;
};

/** A refusal the policy shows: the code its sentence is found by. */
export class Refusal {
  constructor(readonly code: string) {}
}

/** Whether policy `P` certainly leaves field `K` out, or false. */
type LeavesOut<P, K extends keyof ReadPolicy> = [P[K & keyof P]] extends [undefined | false] ? true : false;

/**
 * What a read under policy `P` can answer besides its value. Only a field the
 * policy certainly leaves out drops its answer, so a policy typed wider than
 * a literal keeps every answer it might give.
 */
export type PageRead<T, P> =
  | T
  | (LeavesOut<P, "absent"> extends true ? never : null)
  | (LeavesOut<P, "bestEffort"> extends true ? never : null)
  | (LeavesOut<P, "shown"> extends true ? never : Refusal);

/**
 * One request per path per render, so a layout, its page and their metadata
 * share it; `serverRequest`'s deadline signal opts every call out of Next's
 * own fetch memoisation.
 */
const request = cache((path: string) => serverRequest(path));

/**
 * Reads `path` for a page and parses the answer, answering a refusal as
 * `policy` declares. A best-effort read's parse failure is null; a required
 * one's goes to the error boundary.
 */
export async function readPage<T, const P extends ReadPolicy>(
  path: string,
  parse: (payload: unknown) => T,
  policy: P,
): Promise<PageRead<T, P>> {
  const read = async (): Promise<T | null | Refusal> => {
    let payload: unknown;
    try {
      payload = await request(path);
    } catch (error: unknown) {
      return answer(error, policy);
    }
    return parse(payload);
  };

  return (await (policy.bestEffort ? bestEffort(read, path) : read())) as PageRead<T, P>;
}

/**
 * An API refusal as the policy answers it, or thrown on; anything else, Next's
 * control flow included, is thrown untouched.
 */
function answer(error: unknown, policy: ReadPolicy): null | Refusal {
  const target = authRecoveryRedirect(error, policy.here);
  if (target) redirect(target);

  if (error instanceof ApiError) {
    const { code } = error;
    if (policy.absent?.includes(code)) return null;
    if (policy.shown?.includes(code)) return new Refusal(code);
    const missing =
      code === "not_found" || policy.missing?.includes(code) || (policy.hideForbidden === true && code === "forbidden");
    if (missing) {
      if (policy.bestEffort) return null;
      notFound();
    }
  }
  throw error;
}
