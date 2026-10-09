import type { Dictionary } from "./dictionary";

/**
 * Which dictionary sections each part of the tree can reach, and the
 * server-side narrowing done before the dictionary becomes a prop.
 *
 * Not a client module: a Server Component calling a function exported from a
 * "use client" file gets a client reference and throws. `client.tsx` keeps the
 * provider and the hook and reads the section lists from here.
 * `scopes.test.ts` pins this boundary, which jsdom cannot see.
 */

/** Every screen, because `app/error.tsx` catches a failure on any of them. */
export const APP_SECTIONS = ["screens"] as const;

/** The participant's own group: `/my`, `/open`, and the play workspace. */
export const PARTICIPANT_SECTIONS = ["participant"] as const;

/**
 * The profile, shared by both audiences. Includes `participant` because its
 * boundary borrows the retry wording from there (`app/(session)/error.tsx`).
 */
export const SESSION_SECTIONS = ["profile", "participant"] as const;

/** The constructor's group. */
export const ADMIN_SECTIONS = ["contests", "accounts", "audit", "settings"] as const;

export type Slice<Sections extends readonly (keyof Dictionary)[]> = Pick<Dictionary, Sections[number]>;

/**
 * Built through a mutable record and cast: the dictionary is deeply readonly
 * and `section` is a union, so a typed per-key assignment cannot be written.
 */
function slice<const Sections extends readonly (keyof Dictionary)[]>(
  dict: Dictionary,
  sections: Sections,
): Slice<Sections> {
  const sliced: Record<string, unknown> = {};
  for (const section of sections) sliced[section] = dict[section];
  return sliced as Slice<Sections>;
}

export const selectApp = (dict: Dictionary) => slice(dict, APP_SECTIONS);
export const selectParticipant = (dict: Dictionary) => slice(dict, PARTICIPANT_SECTIONS);
export const selectSession = (dict: Dictionary) => slice(dict, SESSION_SECTIONS);
export const selectAdmin = (dict: Dictionary) => slice(dict, ADMIN_SECTIONS);
