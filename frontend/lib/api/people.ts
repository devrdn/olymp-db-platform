import { z } from "zod";

/**
 * The wire shapes of who runs a contest and who takes part in it.
 *
 * Two audiences, two endpoints, two permissions — and one module, because a
 * screen that shows staff almost always shows participants beside them, and
 * splitting a five-field schema from a seven-field one buys nothing.
 */

export const MANAGER_ROLES = ["owner", "manager"] as const;
export type ManagerRole = (typeof MANAGER_ROLES)[number];

export const REGISTRATION_STATUSES = [
  "registered",
  "active",
  "finished",
  "disqualified",
] as const;
export type RegistrationStatus = (typeof REGISTRATION_STATUSES)[number];

export const managerSchema = z
  .object({
    user_id: z.string(),
    login: z.string(),
    full_name: z.string(),
    role: z.enum(MANAGER_ROLES),
    granted_at: z.string(),
  })
  .transform((raw) => ({
    userId: raw.user_id,
    login: raw.login,
    fullName: raw.full_name,
    role: raw.role,
    grantedAt: raw.granted_at,
  }));

export type Manager = z.infer<typeof managerSchema>;

export const managerListSchema = z.object({ items: z.array(managerSchema) });

export const participantSchema = z
  .object({
    registration_id: z.string(),
    user_id: z.string(),
    login: z.string(),
    full_name: z.string(),
    status: z.enum(REGISTRATION_STATUSES),
    started_at: z.string().optional(),
    finished_at: z.string().optional(),
    total_score: z.number(),
  })
  .transform((raw) => ({
    registrationId: raw.registration_id,
    userId: raw.user_id,
    login: raw.login,
    fullName: raw.full_name,
    status: raw.status,
    startedAt: raw.started_at,
    finishedAt: raw.finished_at,
    totalScore: raw.total_score,
  }));

export type Participant = z.infer<typeof participantSchema>;

export const participantListSchema = z.object({
  items: z.array(participantSchema),
  total: z.number(),
});

/**
 * What an import actually did.
 *
 * A partial success, and honestly so: one typo in a list of three hundred
 * student numbers must not reject the other two hundred and ninety-nine. Every
 * line that did not go in is named with its reason, so it can be found again
 * in the spreadsheet it came from.
 */
export const importResultSchema = z
  .object({
    added: z.number(),
    skipped: z.array(z.object({ ref: z.string(), reason: z.string() })),
  })
  .transform((raw) => ({ added: raw.added, skipped: raw.skipped }));

export type ImportResult = z.infer<typeof importResultSchema>;

/**
 * Whether this participant can still be removed rather than disqualified.
 *
 * Someone who has started cannot be deleted: their queries and answers are
 * part of the record of the contest. Excluding them is a disqualification,
 * which keeps everything they did.
 */
export function removable(participant: Participant): boolean {
  return participant.status === "registered";
}

/**
 * Turns a pasted list into the logins the import endpoint takes.
 *
 * Authors arrive with a column copied out of a spreadsheet, so newlines,
 * commas, semicolons and stray spaces all have to count as separators.
 * Duplicates are dropped here rather than sent: the API would skip the second
 * one as `already_enrolled` and report a failure for something the author
 * never asked for twice.
 */
export function parseLogins(pasted: string): string[] {
  const seen = new Set<string>();
  for (const raw of pasted.split(/[\s,;]+/)) {
    const login = raw.trim();
    if (login) seen.add(login);
  }
  return [...seen];
}
