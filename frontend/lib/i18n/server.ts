import { locale as rootLocale } from "next/root-params";

import { DEFAULT_LOCALE, LOCALES, type Locale } from "./config";
import { getDictionary, type Dictionary } from "./dictionary";

/**
 * The active language, for Server Components.
 *
 * The locale segment sits above the root layout, which makes it a Next root
 * parameter: any Server Component can read it without the value being threaded
 * through props. The value still comes off the URL, so it is never guessed.
 */
export async function activeLocale(): Promise<Locale> {
  const segment = await rootLocale();
  return LOCALES.includes(segment as Locale) ? (segment as Locale) : DEFAULT_LOCALE;
}

export async function activeDictionary(): Promise<Dictionary> {
  return getDictionary(await activeLocale());
}
