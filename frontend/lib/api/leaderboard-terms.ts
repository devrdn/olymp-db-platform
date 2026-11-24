/**
 * The vocabulary of a contest's table, apart from the wire schemas (see
 * content-terms.ts for why).
 */

/** The most registrations one results export carries (`leaderboard.DefaultMaxRows`). */
export const RESULTS_MAX_ROWS = 2000;

/** How many identity hues the palette carries (docs/design/SPEC.md §3.5). */
export const IDENTITY_HUES = 6;

/**
 * A label's identity hue, 1 to 6, stable across reads and screens. FNV-1a over
 * the UTF-16 code units.
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
 * The submitted freeze as `freeze_min`: a number sets it, `null` clears it,
 * and `undefined` sends no key, which is what a locked (disabled, so absent)
 * field must mean. A non-whole or non-positive amount is refused, not rounded.
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
