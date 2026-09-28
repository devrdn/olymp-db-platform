import { z } from "zod";

import { CONTEST_STATUSES } from "./contests";

/**
 * The two reads behind the front page
 * (`/public/…`, docs/ARCHITECTURE.md §9.6).
 *
 * Both are open: no session, no cookie, nothing about the caller. That is the
 * whole point of them, and it is also the boundary of what they may carry —
 * a contest's name, its state, its window and whether its table is open, and
 * never a login, a roster or a draft's schedule.
 *
 * Parsed here rather than trusted, for the same reason every other wire shape
 * in this directory is: the API speaks snake_case and a renamed field should
 * surface with its name in the message, not as `undefined` inside a component
 * two files away.
 */

export const publicStatsSchema = z.object({
  contests: z.number(),
  participants: z.number(),
  queries: z.number(),
  solved: z.number(),
});

export type PublicStats = z.infer<typeof publicStatsSchema>;

/**
 * One contest on the public list.
 *
 * The status enum is the product's whole set rather than the four the route
 * selects. The narrower one would be a second, quietly different definition
 * of "what states a contest has", and its only effect on a wrong answer from
 * the API would be to throw the other five rows away with the bad one. What
 * keeps a draft off this page is the server's own selection, which is where
 * the rule belongs — a client-side enum protects nobody, since the body has
 * already crossed the wire by the time it is checked.
 *
 * `starts_at` and `ends_at` are absent on a contest with no window yet, which
 * is why they are optional and why the row has a phrase for saying so.
 *
 * `cover_hash` is absent on a contest nobody uploaded a picture for, and that
 * absence is not a gap to apologise for: the card draws such a contest a cover
 * of its own from its identifier. The hash rather than an address, because the
 * address is built here (`coverHref`) and carries the hash in it — which is
 * what makes a replaced cover a new address rather than a year of somebody's
 * cache holding the old picture.
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

/** The list names its rows `items`, as every other list this API serves does. */
export const publicContestsSchema = z.object({
  items: z.array(publicContestSchema),
});

export type PublicContests = z.infer<typeof publicContestsSchema>;
