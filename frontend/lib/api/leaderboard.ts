import { z } from "zod";

import { SCORINGS } from "./contests-terms";

/**
 * The contest's table, as the API sends it to its three audiences
 * (docs/superpowers/specs/2026-09-13-leaderboard-design.md).
 *
 * The public and participant copies share one shape: the participant's only
 * extra is `is_you`, and neither carries an identifier of any kind. The staff
 * copy is a different shape on purpose — it names people twice over and lists
 * the disqualified, so a component written for one can never be handed the
 * other by accident.
 */

export const STANDINGS_STATES = [
  "not_started",
  "live",
  "frozen",
  "final",
] as const;
export type StandingsState = (typeof STANDINGS_STATES)[number];

/**
 * The ICPC grid's cell states (docs/superpowers/specs/2026-09-13-icpc-scoring-design.md,
 * the section on grid cells). `cells` and `questions` are present only when
 * `scoring` is `"icpc"` — `points` and `winner` rows carry neither.
 */
export const CELL_STATES = ["solved", "failed", "pending", "untried"] as const;
export type CellState = (typeof CELL_STATES)[number];

const cellSchema = z.object({
  state: z.enum(CELL_STATES),
  // Solved: the 1-based attempt the correct answer was on. Failed: the
  // number of wrong attempts. Pending: the wrong attempts before the freeze,
  // absent when there were none.
  attempts: z.number().optional(),
  // Solved only: the minute of the correct answer.
  minute: z.number().optional(),
  // Solved only: the earliest solve of this question among placed rows.
  first: z.boolean().optional(),
  // Pending only: the attempts made in [freeze_at, now).
  pending: z.number().optional(),
});

export type StandingsCell = z.infer<typeof cellSchema>;

const rowSchema = z
  .object({
    place: z.number().nullable(),
    label: z.string(),
    deleted: z.boolean().optional(),
    points: z.number(),
    solved: z.number(),
    penalty: z.number().optional(),
    cells: z.array(cellSchema).optional(),
    last_scored_at: z.string().optional(),
    winner: z.boolean().optional(),
    is_you: z.boolean().optional(),
  })
  .transform((raw) => ({
    place: raw.place,
    label: raw.label,
    deleted: raw.deleted ?? false,
    points: raw.points,
    solved: raw.solved,
    penalty: raw.penalty,
    cells: raw.cells,
    lastScoredAt: raw.last_scored_at,
    winner: raw.winner ?? false,
    isYou: raw.is_you ?? false,
  }));

export type StandingsRow = z.infer<typeof rowSchema>;

export const standingsSchema = z
  .object({
    state: z.enum(STANDINGS_STATES),
    scoring: z.enum(SCORINGS),
    title: z.string(),
    frozen_at: z.string().optional(),
    ends_at: z.string().optional(),
    generated_at: z.string(),
    truncated: z.boolean(),
    questions: z.array(z.string()).optional(),
    rows: z.array(rowSchema),
  })
  .transform((raw) => ({
    state: raw.state,
    scoring: raw.scoring,
    title: raw.title,
    frozenAt: raw.frozen_at,
    endsAt: raw.ends_at,
    generatedAt: raw.generated_at,
    truncated: raw.truncated,
    questions: raw.questions,
    rows: raw.rows,
  }));

export type Standings = z.infer<typeof standingsSchema>;

const staffRowSchema = z
  .object({
    place: z.number().nullable(),
    login: z.string(),
    full_name: z.string(),
    deleted: z.boolean().optional(),
    disqualified: z.boolean().optional(),
    points: z.number(),
    solved: z.number(),
    penalty: z.number().optional(),
    cells: z.array(cellSchema).optional(),
    last_scored_at: z.string().optional(),
    winner: z.boolean().optional(),
  })
  .transform((raw) => ({
    place: raw.place,
    login: raw.login,
    fullName: raw.full_name,
    deleted: raw.deleted ?? false,
    disqualified: raw.disqualified ?? false,
    points: raw.points,
    solved: raw.solved,
    penalty: raw.penalty,
    cells: raw.cells,
    lastScoredAt: raw.last_scored_at,
    winner: raw.winner ?? false,
  }));

export type StaffStandingsRow = z.infer<typeof staffRowSchema>;

export const staffStandingsSchema = z
  .object({
    shown: z.object({
      state: z.enum(STANDINGS_STATES),
      frozen_at: z.string().optional(),
    }),
    scoring: z.enum(SCORINGS),
    freeze_min: z.number().nullable(),
    names: z.enum(["login", "full_name"]),
    revealed_at: z.string().optional(),
    generated_at: z.string(),
    truncated: z.boolean(),
    questions: z.array(z.string()).optional(),
    rows: z.array(staffRowSchema),
  })
  .transform((raw) => ({
    shown: { state: raw.shown.state, frozenAt: raw.shown.frozen_at },
    scoring: raw.scoring,
    freezeMin: raw.freeze_min,
    names: raw.names,
    revealedAt: raw.revealed_at,
    generatedAt: raw.generated_at,
    truncated: raw.truncated,
    questions: raw.questions,
    rows: raw.rows,
  }));

export type StaffStandings = z.infer<typeof staffStandingsSchema>;

/** How many identity hues the palette carries (docs/design/SPEC.md §3.5). */
export const IDENTITY_HUES = 6;

/**
 * Which identity hue a label wears: 1 to 6, the same for the same label on
 * every read and on every screen, so a person keeps their colour between the
 * play tab and the public page.
 *
 * FNV-1a over the UTF-16 code units — small, dependency-free and spread well
 * enough that a roster of forty lands on every hue.
 */
export function identityHue(label: string): number {
  let hash = 0x811c9dc5;
  for (let i = 0; i < label.length; i++) {
    hash ^= label.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193);
  }
  return ((hash >>> 0) % IDENTITY_HUES) + 1;
}

export const FREEZE_UNITS = ["minutes", "hours"] as const;
export type FreezeUnit = (typeof FREEZE_UNITS)[number];

/**
 * The freeze the settings form submitted, as the API's `freeze_min`.
 *
 * Three outcomes, because the API tells three apart: a number sets the
 * freeze, `null` clears it, and `undefined` sends no key at all — which is
 * what a locked field (the contest has started) must do, since a disabled
 * input submits nothing and "nothing" must not read as "clear it".
 *
 * A non-whole or non-positive amount is refused rather than rounded: a freeze
 * of "1.5 minutes" quietly becoming two is a setting nobody chose.
 */
export function freezeFromForm(
  mode: FormDataEntryValue | null,
  amount: FormDataEntryValue | null,
  unit: FormDataEntryValue | null,
): { ok: true; value: number | null | undefined } | { ok: false } {
  if (mode === null) return { ok: true, value: undefined };
  if (mode === "none") return { ok: true, value: null };

  const raw = String(amount ?? "").trim();
  if (!/^\d+$/.test(raw)) return { ok: false };
  const whole = Number(raw);
  if (whole < 1) return { ok: false };
  return { ok: true, value: unit === "hours" ? whole * 60 : whole };
}

/** The stored freeze as the form shows it: hours when it divides into hours. */
export function freezeForForm(freezeMin: number | null): {
  mode: "none" | "before";
  amount: number;
  unit: FreezeUnit;
} {
  if (freezeMin === null) return { mode: "none", amount: 30, unit: "minutes" };
  if (freezeMin % 60 === 0)
    return { mode: "before", amount: freezeMin / 60, unit: "hours" };
  return { mode: "before", amount: freezeMin, unit: "minutes" };
}
