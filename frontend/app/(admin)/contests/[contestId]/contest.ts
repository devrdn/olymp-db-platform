import { notFound, redirect } from "next/navigation";

import { ApiError } from "@/lib/api/client";
import { contestSchema, type Contest } from "@/lib/api/contests";
import { isId } from "@/lib/api/ids";
import { serverRequest } from "@/lib/api/server";
import { authRecoveryRedirect } from "@/lib/auth/guard";

/**
 * Loading one contest, for every screen in its workspace.
 *
 * Written once because every tab needs the same four decisions and getting one
 * of them wrong on one tab is exactly the kind of difference nobody notices
 * until it matters:
 *
 * - A segment that is not an identifier never becomes a request. The address
 *   is wrong, and spending a round trip to be told 400 only delays saying so.
 * - A dead session goes back to sign-in and an account still on its one-time
 *   password goes to the password screen. Neither is a failure a retry fixes,
 *   which is all the error boundary could offer.
 * - `forbidden` is answered as "no such address", not as "not yours". A
 *   contest existing is not the business of an account that may not see it —
 *   the same reasoning the API applies to a question belonging to another
 *   contest, where it answers 404 rather than 403.
 * - Anything else is thrown on to the error boundary, where a retry is honest.
 */
export async function loadContest(contestId: string): Promise<Contest> {
  if (!isId(contestId)) notFound();

  const payload = await serverRequest(`/contests/${contestId}`).catch((error: unknown) => {
    const target = authRecoveryRedirect(error, `/contests/${contestId}`);
    if (target) redirect(target);

    if (error instanceof ApiError && (error.code === "not_found" || error.code === "forbidden")) {
      notFound();
    }
    throw error;
  });

  return contestSchema.parse(payload);
}

/**
 * The same treatment for anything else hanging off a contest: its story, its
 * questions, its people.
 *
 * `notFoundIsEmpty` is the difference between "this contest has no story yet",
 * which is an ordinary state of a draft, and "this address is wrong". The
 * first must not become a 404 page — the author is about to write one.
 */
export async function loadContestResource<T>(
  contestId: string,
  path: string,
  parse: (payload: unknown) => T,
  options: { notFoundIsEmpty?: true } = {},
): Promise<T | null> {
  if (!isId(contestId)) notFound();

  const payload = await serverRequest(`/contests/${contestId}${path}`).catch((error: unknown) => {
    const target = authRecoveryRedirect(error, `/contests/${contestId}`);
    if (target) redirect(target);

    if (error instanceof ApiError) {
      if (options.notFoundIsEmpty && (error.code === "not_found" || error.code === "story_not_found")) {
        return null;
      }
      if (error.code === "not_found" || error.code === "forbidden") notFound();
    }
    throw error;
  });

  return payload === null ? null : parse(payload);
}
