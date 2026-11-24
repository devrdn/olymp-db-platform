import type { Revalidation } from "@/lib/api/server";

/**
 * What a write to a contest makes stale: the workspace layout and every screen
 * beneath it, since the header, the publish gate and the settings form's
 * version all read the contest; with `register`, the contest register too, for
 * a write that changes what it lists. Every action that writes a contest
 * revalidates through this, or the organiser's next settings save is refused
 * as made against an older version.
 */
export function contestChanged(contestId: string, { register = false }: { register?: boolean } = {}): Revalidation[] {
  const workspace = { layout: `/contests/${contestId}` };
  return register ? ["/contests", workspace] : [workspace];
}
