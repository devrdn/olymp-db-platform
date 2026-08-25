import { cookies } from "next/headers";

import { LOCALE_COOKIE, type Locale } from "./config";
import { getDictionary, type Dictionary } from "./dictionary";
import { readLocale } from "./locale";

/**
 * The active language, for Server Components.
 *
 * Read on the server before anything renders, which is why the choice lives in
 * a cookie: localStorage would arrive after the page had already been built,
 * and after the API had been asked for content in the wrong language.
 */
export async function activeLocale(): Promise<Locale> {
  const jar = await cookies();
  return readLocale(jar.get(LOCALE_COOKIE)?.value);
}

export async function activeDictionary(): Promise<Dictionary> {
  return getDictionary(await activeLocale());
}
