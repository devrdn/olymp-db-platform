export const LOCALES = ["en", "ro", "ru"] as const;
export const DEFAULT_LOCALE = "en";
export type Locale = (typeof LOCALES)[number];

/** A cookie, not localStorage: the server needs the language before rendering. */
export const LOCALE_COOKIE = "dbcontest_locale";

/** Names shown in the switcher, in the language each one names. */
export const LOCALE_NAMES: Record<Locale, string> = {
  en: "English",
  ro: "Română",
  ru: "Русский",
};
