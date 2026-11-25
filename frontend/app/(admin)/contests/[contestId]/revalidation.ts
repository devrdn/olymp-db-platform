import type { Revalidation } from "@/lib/api/server";

/**
 * What a write to a contest makes stale: the workspace layout and every screen
 * beneath it, since the header, the publish gate and the version every form
 * that saves the contest or its content sends all read the contest; with
 * `register`, the contest register too, for a write that changes what it
 * lists. Every action that writes a contest revalidates through this, or the
 * organiser's next save of one of those forms is refused as made against an
 * older version.
 */
export function contestChanged(contestId: string, { register = false }: { register?: boolean } = {}): Revalidation[] {
  const workspace = { layout: `/contests/${contestId}` };
  return register ? ["/contests", workspace] : [workspace];
}
