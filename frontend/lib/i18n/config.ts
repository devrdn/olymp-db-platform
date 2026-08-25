export const LOCALES = ["en", "ro", "ru"] as const;
export const DEFAULT_LOCALE = "en";
export type Locale = (typeof LOCALES)[number];
