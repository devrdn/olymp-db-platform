import type { Locale } from "./config";
import type { Dictionary } from "./dictionaries/en";

/** One dictionary per locale, loaded on demand so a page ships only its own language. */
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
