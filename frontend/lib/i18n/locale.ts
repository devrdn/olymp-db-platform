import { DEFAULT_LOCALE, LOCALES, type Locale } from "./config";

/**
 * The language from its single source, the visitor's cookie: no
 * Accept-Language, no locale in the URL. Anything but a declared locale reads
 * as absent, so a cookie value is never used as a path.
 */
export function readLocale(stored: string | undefined | null): Locale {
  return LOCALES.includes(stored as Locale) ? (stored as Locale) : DEFAULT_LOCALE;
}
