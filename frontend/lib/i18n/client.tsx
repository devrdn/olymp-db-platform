"use client";

import { createContext, useContext } from "react";

import { ADMIN_SECTIONS, APP_SECTIONS, PARTICIPANT_SECTIONS, SESSION_SECTIONS } from "./scopes";

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
 *
 * What changed (finding 5): the dictionary handed over is a *scope*, not the
 * whole book. One provider at the root carrying every section made the entire
 * dictionary a client prop on every screen in the product — measured at
 * 50,727 bytes for `en`, about half of the 99 KB RSC payload of
 * `/contests/[contestId]/play`, on the one route hundreds of participants
 * load within the same minute. The sections nobody on that screen can read —
 * the contest workspace's own vocabulary, the account register, the audit
 * trail, the settings, the contest register — came to 31.8 KB of it, 63% of
 * the dictionary, crossing the wire for nothing.
 *
 * A scope names the sections one part of the tree can reach, and the narrowing
 * happens **on the server**, before the value is ever a prop — in `scopes.ts`,
 * not here. A Server Component cannot call a function exported from this file:
 * it gets a client reference, and calling it throws. The narrowing lived here
 * once, and every page of the product answered 500 until it moved.
 * That is the whole mechanism: a provider that took the whole dictionary and
 * narrowed it in the browser would have shipped the whole dictionary to do
 * it. The type is what keeps the two honest — a boundary that reads a section
 * its own scope does not name does not compile, and adding the section to the
 * scope is the same edit that puts it on the wire.
 *
 * Scopes nest. `/play` sits under the root's and the participant group's, and
 * the two slices reference the same section objects, so nothing is serialised
 * twice.
 */
export type DictionaryScope<Section extends keyof Dictionary> = {
  Provider: (props: {
    dict: Pick<Dictionary, Section>;
    locale: Locale;
    children: React.ReactNode;
  }) => React.ReactNode;
  /** The scope's own sections, and the active language, for a client boundary under it. */
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

/**
 * Every screen in the product, because `app/error.tsx` catches a failure on
 * any of them. One section, and a small one.
 */
export const AppDictionary = dictionaryScope("app", APP_SECTIONS);

/** The participant's own group: `/my`, `/open`, and the play workspace. */
export const ParticipantDictionary = dictionaryScope("participant", PARTICIPANT_SECTIONS);

/**
 * The profile, which both audiences share. It carries `participant` as well
 * as `profile` because its own boundary borrows the retry wording from there
 * rather than repeating it — see `app/(session)/error.tsx`.
 */
export const SessionDictionary = dictionaryScope("session", SESSION_SECTIONS);

/** The constructor's group, whose four boundaries each name their own screen. */
export const AdminDictionary = dictionaryScope("admin", ADMIN_SECTIONS);


/**
 * The providers, each as its own top-level export.
 *
 * A Server Component renders these, and it can only render a top-level export
 * of a "use client" module: that is what becomes a client reference. Reaching
 * one as a member — `<AppDictionary.Provider>` — yields `undefined` on the
 * server, and the root layout answered 500 with "Element type is invalid"
 * until they were exported here. The scope objects above remain for the hook,
 * which only ever runs in the browser, inside a client boundary.
 */
export const AppDictionaryProvider = AppDictionary.Provider;
export const ParticipantDictionaryProvider = ParticipantDictionary.Provider;
export const SessionDictionaryProvider = SessionDictionary.Provider;
export const AdminDictionaryProvider = AdminDictionary.Provider;
