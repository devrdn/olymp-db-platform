import { cookies } from "next/headers";

import { LOCALE_COOKIE, type Locale } from "./config";
import { getDictionary, type Dictionary } from "./dictionary";
import { readLocale } from "./locale";

/** The active language, for Server Components. */
export async function activeLocale(): Promise<Locale> {
  const jar = await cookies();
  return readLocale(jar.get(LOCALE_COOKIE)?.value);
}

export async function activeDictionary(): Promise<Dictionary> {
  return getDictionary(await activeLocale());
}
