import { z } from "zod";


export { removable } from "./people-terms";


/** The wire shapes of who runs a contest and who takes part in it. */

/** The roles a contest's staff hold. */
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
 * What an enrolment import did. Partial success: one typo must not reject the
 * rest, and every skipped line comes back with its reason.
 */
export const importResultSchema = z
  .object({
    added: z.number(),
    skipped: z.array(z.object({ ref: z.string(), reason: z.string() })),
  })
  .transform((raw) => ({ added: raw.added, skipped: raw.skipped }));

export type ImportResult = z.infer<typeof importResultSchema>;

/**
 * Splits a pasted spreadsheet column into logins on whitespace, commas and
 * semicolons. Duplicates are dropped so the API does not report the second as
 * `already_enrolled`.
 */
export function parseLogins(pasted: string): string[] {
  const seen = new Set<string>();
  for (const raw of pasted.split(/[\s,;]+/)) {
    const login = raw.trim();
    if (login) seen.add(login);
  }
  return [...seen];
}
