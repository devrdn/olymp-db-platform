export const LOCALES = ["en", "ro", "ru"] as const;
export const DEFAULT_LOCALE = "en";
export type Locale = (typeof LOCALES)[number];

/**
 * Where a chosen language is remembered.
 *
 * A cookie rather than localStorage: the choice is needed on the server before
 * anything renders, and the API is asked for content in that language in the
 * same pass.
 */
export const LOCALE_COOKIE = "dbcontest_locale";

/** Names shown in the switcher, in the language each one names. */
export const LOCALE_NAMES: Record<Locale, string> = {
  en: "English",
  ro: "Română",
  ru: "Русский",
};
