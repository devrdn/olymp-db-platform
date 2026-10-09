import { z } from "zod";

import { CONTEST_STATUSES } from "./contests";

/**
 * The two open reads behind the front page (`/public/…`, docs/ARCHITECTURE.md
 * §9.6). No session, so they carry a contest's name, state, window and whether
 * its table is open, never a login, a roster or a draft's schedule.
 */

export const publicStatsSchema = z.object({
  contests: z.number(),
  participants: z.number(),
  queries: z.number(),
  solved: z.number(),
});

export type PublicStats = z.infer<typeof publicStatsSchema>;

/**
 * One contest on the public list. The status enum is the full set: keeping
 * drafts off this page is the server's selection, and a client-side enum
 * protects nobody once the body has crossed the wire. The window is absent
 * until set; `cover_hash` is absent for a drawn cover, and `coverHref` builds
 * the address from it.
 */
export const publicContestSchema = z
  .object({
    id: z.string(),
    title: z.string(),
    status: z.enum(CONTEST_STATUSES),
    starts_at: z.string().optional(),
    ends_at: z.string().optional(),
    table_open: z.boolean(),
    cover_hash: z.string().optional(),
    cover_attribution: z.string().optional(),
  })
  .transform((raw) => ({
    id: raw.id,
    title: raw.title,
    status: raw.status,
    startsAt: raw.starts_at,
    endsAt: raw.ends_at,
    tableOpen: raw.table_open,
    coverHash: raw.cover_hash,
    coverAttribution: raw.cover_attribution,
  }));

export type PublicContest = z.infer<typeof publicContestSchema>;

export const publicContestsSchema = z.object({
  items: z.array(publicContestSchema),
});

export type PublicContests = z.infer<typeof publicContestsSchema>;
