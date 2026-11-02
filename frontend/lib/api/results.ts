import { z } from "zod";

import { CONTEST_STATUSES } from "./contests";
import { SCORINGS } from "./contests-terms";
import { prizeSchema } from "./leaderboard";

/**
 * The results report the staff preview shows and the export serves
 * (`GET …/results`, `POST …/results/export` with `format: "json"`). The
 * wire is snake_case; the parsed type is camelCase.
 */

export const REPORT_STATUSES = ["preliminary", "final", "archived"] as const;
export type ReportStatus = (typeof REPORT_STATUSES)[number];

export const RESULTS_FIELDS = ["full_name", "login", "email"] as const;
export type ResultsField = (typeof RESULTS_FIELDS)[number];

export const RESULTS_DETAILS = ["summary", "detailed"] as const;
export type ResultsDetail = (typeof RESULTS_DETAILS)[number];

const contestSchema = z
  .object({
    id: z.string(),
    titles: z.record(z.string(), z.string()),
    default_language: z.string(),
    status: z.enum(CONTEST_STATUSES),
    scoring: z.enum(SCORINGS),
    icpc_penalty_min: z.number(),
    prize_places: z.object({ first: z.number(), second: z.number(), third: z.number() }),
    starts_at: z.string().optional(),
    ends_at: z.string().optional(),
  })
  .transform((raw) => ({
    id: raw.id,
    titles: raw.titles,
    defaultLanguage: raw.default_language,
    status: raw.status,
    scoring: raw.scoring,
    icpcPenaltyMin: raw.icpc_penalty_min,
    prizePlaces: raw.prize_places,
    startsAt: raw.starts_at,
    endsAt: raw.ends_at,
  }));

const questionSchema = z.object({
  position: z.number(),
  // ICPC only.
  letter: z.string().optional(),
  bonus: z.boolean(),
  titles: z.record(z.string(), z.string()),
});

// ICPC cells always carry `solved` and `wrong`, `minute` only when solved;
// the other modes carry `solved` and `attempts`, and `points` except on a
// bonus question.
const cellSchema = z.object({
  solved: z.boolean(),
  minute: z.number().optional(),
  wrong: z.number().optional(),
  points: z.number().optional(),
  attempts: z.number().optional(),
});

const rowSchema = z
  .object({
    registration_id: z.string(),
    place: z.number().nullable(),
    prize: prizeSchema,
    full_name: z.string().optional(),
    login: z.string().optional(),
    email: z.string().optional(),
    deleted: z.boolean().optional(),
    disqualified: z.boolean(),
    winner: z.boolean().optional(),
    solved: z.number(),
    // `penalty` is ICPC's and `points` every other mode's; the other is null.
    penalty: z.number().nullable(),
    points: z.number().nullable(),
    last_solved_at: z.string().nullable(),
    bonus_credit: z.number().nullable(),
    cells: z.array(cellSchema).optional(),
  })
  .transform((raw) => ({
    id: raw.registration_id,
    place: raw.place,
    prize: raw.prize,
    fullName: raw.full_name,
    login: raw.login,
    email: raw.email,
    deleted: raw.deleted ?? false,
    disqualified: raw.disqualified,
    winner: raw.winner ?? false,
    solved: raw.solved,
    penalty: raw.penalty,
    points: raw.points,
    lastSolvedAt: raw.last_solved_at,
    bonusCredit: raw.bonus_credit,
    cells: raw.cells ?? [],
  }));

export const resultsReportSchema = z
  .object({
    contest: contestSchema,
    status: z.enum(REPORT_STATUSES),
    hidden_by_freeze: z.boolean(),
    generated_at: z.string(),
    registered: z.number(),
    detail: z.enum(RESULTS_DETAILS),
    fields: z.array(z.enum(RESULTS_FIELDS)),
    bonus_credit: z.boolean(),
    questions: z.array(questionSchema),
    rows: z.array(rowSchema),
    fingerprint: z.string(),
    // Only the preview says it.
    may_export_email: z.boolean().optional(),
  })
  .transform((raw) => ({
    contest: raw.contest,
    status: raw.status,
    hiddenByFreeze: raw.hidden_by_freeze,
    generatedAt: raw.generated_at,
    registered: raw.registered,
    detail: raw.detail,
    fields: raw.fields,
    bonusCredit: raw.bonus_credit,
    questions: raw.questions,
    rows: raw.rows,
    fingerprint: raw.fingerprint,
    mayExportEmail: raw.may_export_email ?? false,
  }));

export type ResultsReport = z.infer<typeof resultsReportSchema>;
export type ResultsRow = ResultsReport["rows"][number];
export type ResultsCell = z.infer<typeof cellSchema>;

/** The body of `POST …/results/export`. `csv` applies to the CSV format only. */
export type ResultsExportBody = {
  format: "csv" | "json";
  registrations: string[];
  detail: ResultsDetail;
  fields: ResultsField[];
  csv?: { excel: boolean };
};
