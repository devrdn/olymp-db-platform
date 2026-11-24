import { fillBounds } from "./bounds";
import type { Locale } from "./config";
import type { Dictionary } from "./dictionaries/en";

/** One dictionary per locale, loaded on demand so a page ships only its own language. */
const LOADERS: Record<Locale, () => Promise<{ default: Dictionary }>> = {
  en: () => import("./dictionaries/en"),
  ro: () => import("./dictionaries/ro"),
  ru: () => import("./dictionaries/ru"),
};

/** Filled once per locale: the bounds are constants, so every request reads the same text. */
const filled = new Map<Locale, Promise<Dictionary>>();

export function getDictionary(locale: Locale): Promise<Dictionary> {
  const known: Locale = Object.hasOwn(LOADERS, locale) ? locale : "en";
  let dictionary = filled.get(known);
  if (!dictionary) {
    dictionary = LOADERS[known]().then(
      (module) => fillBounds(module.default, known),
      (error: unknown) => {
        // A failed load is not kept, so the next request tries again.
        filled.delete(known);
        throw error;
      },
    );
    filled.set(known, dictionary);
  }
  return dictionary;
}

export type { Dictionary };
