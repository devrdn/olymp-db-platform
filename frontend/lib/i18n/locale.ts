import { DEFAULT_LOCALE, LOCALES, type Locale } from "./config";

/**
 * The language, from the one place that holds it.
 *
 * There is a single source: what the visitor chose, kept in a cookie. No
 * precedence chain, no sniffing of Accept-Language, no locale in the URL. A
 * value that is not one of the declared languages is treated as absent rather
 * than trusted, which also stops anything arriving through the cookie from
 * being used as a path.
 *
 * When the API starts exposing `users.locale` (the column exists already), the
 * account becomes the source for a signed-in user and the cookie is left to
 * cover only the sign-in screen. That is a change in this function, nowhere
 * else, which is the point of keeping it to one.
 */
export function readLocale(stored: string | undefined | null): Locale {
  return LOCALES.includes(stored as Locale) ? (stored as Locale) : DEFAULT_LOCALE;
}
