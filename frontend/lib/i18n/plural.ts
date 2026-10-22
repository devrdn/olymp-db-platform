import type { Locale } from "./config";

/**
 * A count's sentence in every form the platform's languages need, keyed by
 * the CLDR plural category: English uses one and other, Romanian adds few,
 * Russian adds few and many. A locale fills every key, repeating a form where
 * its grammar has fewer, so all three dictionaries keep one shape.
 */
export type PluralForms = { one: string; few: string; many: string; other: string };

/** The form `Intl.PluralRules` names for `n` in `locale`, with `{n}` filled in. */
export function plural(forms: PluralForms, n: number, locale: Locale): string {
  const category = new Intl.PluralRules(locale).select(n);
  const form = category in forms ? forms[category as keyof PluralForms] : forms.other;
  return form.replace("{n}", String(n));
}
