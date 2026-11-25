import { notFound } from "next/navigation";

import { contestSchema, type Contest } from "@/lib/api/contests";
import { isId } from "@/lib/api/ids";
import { readPage, type PageRead, type ReadPolicy } from "@/lib/api/page-read";

/**
 * What every workspace screen's read of a contest shares: sign-in leads back
 * to the contest, and `forbidden` is answered as not found, since a contest's
 * existence is not the business of someone who may not see it.
 */
function contestPolicy(contestId: string) {
  return { here: `/contests/${contestId}`, hideForbidden: true } as const;
}

/**
 * Loads one contest for every workspace screen, so all tabs decide the same
 * way. A non-id segment never becomes a request.
 */
export async function loadContest(contestId: string): Promise<Contest> {
  if (!isId(contestId)) notFound();
  return readPage(`/contests/${contestId}`, (payload) => contestSchema.parse(payload), contestPolicy(contestId));
}

/** What a screen adds to the workspace's policy for one of a contest's sub-resources. */
export type ResourcePolicy = Omit<ReadPolicy, "here" | "hideForbidden">;

/**
 * The same for a contest's sub-resources, `path` beneath the contest's own.
 * It starts only once the contest is read (the render's one read of it,
 * shared with the layout and the page), so whatever a screen reads beside the
 * contest, a write landing between the two pairs a newer copy with an older
 * version, which the form's save is refused at, never an older copy with a
 * newer version, which the save would be written over the newer copy with.
 */
export async function loadContestResource<T, const P extends ResourcePolicy = Record<never, never>>(
  contestId: string,
  path: string,
  parse: (payload: unknown) => T,
  policy?: P,
): Promise<PageRead<T, P>> {
  await loadContest(contestId);
  // The answer's type depends only on fields of `policy`, never on the two
  // bound here, but TypeScript cannot follow that through a generic `P`.
  return readPage(`/contests/${contestId}${path}`, parse, { ...policy, ...contestPolicy(contestId) }) as Promise<
    PageRead<T, P>
  >;
}
