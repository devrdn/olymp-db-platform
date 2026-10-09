"use client";

import { createContext, useContext } from "react";

import { ADMIN_SECTIONS, APP_SECTIONS, PARTICIPANT_SECTIONS, SESSION_SECTIONS } from "./scopes";

import type { Locale } from "./config";
import type { Dictionary } from "./dictionary";

/**
 * The dictionary for client boundaries that cannot await it, chiefly
 * `error.tsx`: a layout puts it in context and the boundary reads it.
 *
 * What is handed over is a scope, not the whole dictionary, which was about
 * half the play route's RSC payload. The narrowing happens on the server, in
 * `scopes.ts`, before the value becomes a prop. A boundary that reads a
 * section its scope does not name does not compile.
 *
 * Scopes nest, and nested slices share section objects, so nothing is
 * serialised twice.
 */
export type DictionaryScope<Section extends keyof Dictionary> = {
  Provider: (props: {
    dict: Pick<Dictionary, Section>;
    locale: Locale;
    children: React.ReactNode;
  }) => React.ReactNode;
  use: () => { dict: Pick<Dictionary, Section>; locale: Locale };
};

function dictionaryScope<const Sections extends readonly (keyof Dictionary)[]>(
  name: string,
  sections: Sections,
): DictionaryScope<Sections[number]> {
  type Sliced = Pick<Dictionary, Sections[number]>;
  const Context = createContext<{ dict: Sliced; locale: Locale } | null>(null);

  return {
    Provider({ dict, locale, children }) {
      return <Context.Provider value={{ dict, locale }}>{children}</Context.Provider>;
    },
    use() {
      const value = useContext(Context);
      if (!value) {
        throw new Error(`This needs the ${name} <DictionaryScope.Provider>, normally on the route layout.`);
      }
      return value;
    },
  };
}

// Which sections each scope carries is documented in `scopes.ts`.
export const AppDictionary = dictionaryScope("app", APP_SECTIONS);
export const ParticipantDictionary = dictionaryScope("participant", PARTICIPANT_SECTIONS);
export const SessionDictionary = dictionaryScope("session", SESSION_SECTIONS);
export const AdminDictionary = dictionaryScope("admin", ADMIN_SECTIONS);


/**
 * The providers as top-level exports: a Server Component can render only a
 * top-level export of a "use client" module. As a member
 * (`<AppDictionary.Provider>`) it is `undefined` on the server.
 */
export const AppDictionaryProvider = AppDictionary.Provider;
export const ParticipantDictionaryProvider = ParticipantDictionary.Provider;
export const SessionDictionaryProvider = SessionDictionary.Provider;
export const AdminDictionaryProvider = AdminDictionary.Provider;
