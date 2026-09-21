import { z } from "zod";

import { CONTEST_STATUSES } from "./contests";
import { SCORINGS } from "./contests-terms";

/**
 * The wire shapes of a participant's own profile
 * (`/me/…`, docs/superpowers/specs/2026-09-21-participant-profile-design.md §3).
 *
 * Read on the server, where the session already is, and parsed at the boundary
 * so a contract change surfaces here with the field name in the message rather
 * than as `undefined` three components later. The API speaks snake_case; the
 * interface speaks camelCase, and the mapping happens once, here.
 *
 * Only the two reads the profile screen itself makes. The report and its tabs
 * are a screen of their own and get their own shapes when they arrive.
 */

/** The table's own state, as `leaderboard.Decide` names it. */
export const TABLE_STATES = ["live", "frozen", "final"] as const;
export type TableState = (typeof TABLE_STATES)[number];

export const profileSummarySchema = z.object({
  contests: z.number(),
  finished: z.number(),
  queries: z.number(),
  solved: z.number(),
});

export type ProfileSummary = z.infer<typeof profileSummarySchema>;

/**
 * What the participant scored, in whichever of the two shapes the contest's
 * mode makes a result.
 *
 * `penalty` is ICPC's and is sent in no other mode, which is what makes it the
 * honest discriminator: in ICPC scoring `points` is always zero — the server
 * writes none — so a row that read points there would report nought for four
 * solved questions.
 *
 * There is deliberately no place. Naming one means computing a whole standings
 * table per contest to decorate an overview (design §2.1); `placeOpen` says
 * only whether the report has a place to show, which is true of a final table
 * or a freeze an organiser revealed.
 */
export const profileResultSchema = z
  .object({
    scoring: z.enum(SCORINGS),
    points: z.number(),
    solved: z.number(),
    penalty: z.number().optional(),
    state: z.enum(TABLE_STATES),
    place_open: z.boolean(),
  })
  .transform((raw) => ({
    scoring: raw.scoring,
    points: raw.points,
    solved: raw.solved,
    penalty: raw.penalty,
    state: raw.state,
    placeOpen: raw.place_open,
  }));

export type ProfileResult = z.infer<typeof profileResultSchema>;

export const profileContestSchema = z
  .object({
    contest_id: z.string(),
    title: z.string(),
    status: z.enum(CONTEST_STATUSES),
    starts_at: z.string().optional(),
    ends_at: z.string().optional(),
    /** The caller's own standing on the roster: registered, active, finished, disqualified. */
    registration_status: z.string(),
    /** Whether the contest has ended for this caller, and so whether its report opens. */
    over: z.boolean(),
    /**
     * Absent for a contest that is not over for the caller. Absent rather than
     * zeroed on purpose: a row has to tell "nothing to show yet" from "a
     * result of nought", and during a contest the profile shows nothing of
     * what is happening inside it (design §1).
     */
    result: profileResultSchema.optional(),
  })
  .transform((raw) => ({
    contestId: raw.contest_id,
    title: raw.title,
    status: raw.status,
    startsAt: raw.starts_at,
    endsAt: raw.ends_at,
    registrationStatus: raw.registration_status,
    over: raw.over,
    result: raw.result,
  }));

export type ProfileContest = z.infer<typeof profileContestSchema>;

export const profileContestsSchema = z.object({
  items: z.array(profileContestSchema),
  /** The account is on more contests than one profile carries. */
  truncated: z.boolean(),
});

export type ProfileContests = z.infer<typeof profileContestsSchema>;
