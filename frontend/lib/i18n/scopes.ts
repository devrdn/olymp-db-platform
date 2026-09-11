import type { Dictionary } from "./dictionary";

/**
 * Which sections of the dictionary each part of the tree can reach, and the
 * narrowing a Server Component does before the dictionary becomes a prop.
 *
 * This module is deliberately not a client module. A Server Component cannot
 * call a function exported from a "use client" file: it receives a client
 * reference there, and calling it throws "is not a function". The narrowing
 * lived on the scope object in `client.tsx` once, and every page of the
 * product answered 500 until it moved here. `client.tsx` keeps the half that
 * genuinely runs in the browser — the provider and the hook — and reads the
 * section lists from this file, so the two cannot disagree about what a scope
 * contains. `scopes.test.ts` pins the boundary, which a jsdom run cannot see.
 */

/** Every screen in the product, because `app/error.tsx` catches a failure on any of them. */
export const APP_SECTIONS = ["screens"] as const;

/** The participant's own group: `/my`, `/open`, and the play workspace. */
export const PARTICIPANT_SECTIONS = ["participant"] as const;

/**
 * The profile, which both audiences share. It carries `participant` as well
 * as `profile` because its own boundary borrows the retry wording from there
 * rather than repeating it — see `app/(session)/error.tsx`.
 */
export const SESSION_SECTIONS = ["profile", "participant"] as const;

/** The constructor's group, whose four boundaries each name their own screen. */
export const ADMIN_SECTIONS = ["contests", "accounts", "audit", "settings"] as const;

/** The part of a dictionary a list of sections names. */
export type Slice<Sections extends readonly (keyof Dictionary)[]> = Pick<Dictionary, Sections[number]>;

/**
 * Narrows a whole dictionary to the named sections.
 *
 * Built through a mutable record and handed back as the slice: the dictionary
 * is deeply readonly and `section` is a union of the listed keys rather than
 * one of them, neither of which a per-key assignment can be expressed against.
 * The cast is checked by the return type, and `sections` is the only thing
 * that can be wrong.
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
