"use client";

import { createContext, useContext } from "react";

import type { Locale } from "./config";
import type { Dictionary } from "./dictionary";

/**
 * The dictionary, for the parts of a route that cannot ask the server for it.
 *
 * `error.tsx` is the reason this exists. Next renders it as a Client Component
 * inside the segment's layout, so it can never `await` a dictionary — and a
 * screen that cannot be translated is a screen that gets written in one
 * language and stays that way. Both error screens in this project did exactly
 * that. A layout puts the resolved dictionary in context and the boundary
 * reads it, so all nine states of section 7 speak all three languages.
 */
const DictionaryContext = createContext<{ dict: Dictionary; locale: Locale } | null>(null);

export function DictionaryProvider({
  dict,
  locale,
  children,
}: {
  dict: Dictionary;
  locale: Locale;
  children: React.ReactNode;
}) {
  return (
    <DictionaryContext.Provider value={{ dict, locale }}>{children}</DictionaryContext.Provider>
  );
}

export function useDictionary() {
  const value = useContext(DictionaryContext);
  if (!value) {
    throw new Error("useDictionary() needs a <DictionaryProvider>, normally on the route layout.");
  }
  return value;
}
