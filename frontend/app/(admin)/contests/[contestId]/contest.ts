import { notFound, redirect } from "next/navigation";
import { cache } from "react";

import { ApiError } from "@/lib/api/client";
import { contestSchema, type Contest } from "@/lib/api/contests";
import { isId } from "@/lib/api/ids";
import { serverRequest } from "@/lib/api/server";
import { authRecoveryRedirect } from "@/lib/auth/guard";

/**
 * Loads one contest for every workspace screen, so all tabs decide the same
 * way: a non-id segment never becomes a request; a dead session goes to sign-in
 * and a one-time password to the password screen; `forbidden` is answered as
 * not found, since a contest's existence is not the business of someone who may
 * not see it; anything else goes to the error boundary. React's `cache()`
 * makes it one request per render, since `serverRequest`'s deadline signal
 * opts every call out of Next's own fetch memoisation.
 */
export const loadContest = cache(async (contestId: string): Promise<Contest> => {
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
});

const readContestResource = cache((contestId: string, path: string, notFoundIsEmpty: boolean) =>
  serverRequest(`/contests/${contestId}${path}`).catch((error: unknown) => {
    const target = authRecoveryRedirect(error, `/contests/${contestId}`);
    if (target) redirect(target);

    if (error instanceof ApiError) {
      if (notFoundIsEmpty && (error.code === "not_found" || error.code === "story_not_found")) {
        return null;
      }
      if (error.code === "not_found" || error.code === "forbidden") notFound();
    }
    throw error;
  }),
);

/**
 * The same for a contest's sub-resources. `notFoundIsEmpty` turns a 404 into an
 * empty answer where absence is an ordinary state (a draft without a story).
 * The request is memoised apart from `parse`, which every caller passes as a
 * fresh closure that `cache()` would never match.
 */
export async function loadContestResource<T>(
  contestId: string,
  path: string,
  parse: (payload: unknown) => T,
  options: { notFoundIsEmpty?: true } = {},
): Promise<T | null> {
  if (!isId(contestId)) notFound();

  const payload = await readContestResource(contestId, path, options.notFoundIsEmpty === true);
  return payload === null ? null : parse(payload);
}
