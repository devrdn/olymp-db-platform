import type { Locale } from "./config";
import type { Dictionary } from "./dictionaries/en";

/**
 * One dictionary per locale, loaded on demand so a page ships only the
 * language it renders in.
 *
 * Adding a fourth language is a file here and a code in config: no component
 * changes, mirroring how the server keeps languages in a table rather than in
 * an enum (architecture section 6.2).
 */
const LOADERS: Record<Locale, () => Promise<{ default: Dictionary }>> = {
  en: () => import("./dictionaries/en"),
  ro: () => import("./dictionaries/ro"),
  ru: () => import("./dictionaries/ru"),
};

export async function getDictionary(locale: Locale): Promise<Dictionary> {
  const load = LOADERS[locale] ?? LOADERS.en;
  return (await load()).default;
}

export type { Dictionary };
