import { afterEach, describe, expect, test, vi } from "vitest";

import { BOUNDS } from "./bounds";
import { LOCALES } from "./config";
import { getDictionary } from "./dictionary";
import en from "./dictionaries/en";
import ro from "./dictionaries/ro";
import ru from "./dictionaries/ru";

const RAW = { en, ro, ru };

/** Every string in a dictionary, by its dotted key. */
function sentences(node: unknown, key = ""): [string, string][] {
  if (typeof node === "string") return [[key, node]];
  if (node !== null && typeof node === "object") {
    return Object.entries(node).flatMap(([name, value]) => sentences(value, key ? `${key}.${name}` : name));
  }
  return [];
}

const names = new Set(Object.keys(BOUNDS));

/**
 * The placeholders a screen fills at render, through `fill` or `plural`. A
 * name in a sentence that is in neither this list nor `BOUNDS` is a bound
 * written without its entry, which the dictionary would show unfilled.
 */
const FILLED_AT_RENDER = new Set([
  "action", "added", "address", "attempt", "chars", "column", "columns", "count", "date", "domain",
  "duration", "email", "end", "filename", "fingerprint", "first", "from", "id", "ip", "keys", "letter",
  "login", "m", "max", "minute", "minutes", "n", "name", "percent", "points", "provider", "rate",
  "reason", "received", "registered", "removed", "row", "second", "seconds", "selected", "sent",
  "shown", "size", "start",
  "tab", "table", "target", "targetColumns", "third", "time", "title", "to", "total", "w", "when",
  "who",
]);

/** The bound placeholders a sentence names. */
function boundsIn(sentence: string): string[] {
  return [...sentence.matchAll(/\{(\w+)\}/g)].map((match) => match[1]!).filter((name) => names.has(name));
}

/**
 * The plural category each bound's value had when its Romanian and Russian
 * sentences were written. The noun after a placeholder is fixed in the
 * dictionary ("{n} de caractere", "{n} символов"), so a value that moves into
 * another category needs those sentences reworded, and then this table.
 */
const WRITTEN_FOR: Record<keyof typeof BOUNDS, { ro: Intl.LDMLPluralRule; ru: Intl.LDMLPluralRule }> = {
  announcementMaxChars: { ro: "other", ru: "many" },
  answerMaxChars: { ro: "other", ru: "many" },
  coverAttributionMax: { ro: "other", ru: "many" },
  coverMaxMb: { ro: "few", ru: "many" },
  coverMaxPixels: { ro: "other", ru: "many" },
  feedSettleSeconds: { ro: "few", ru: "few" },
  feedbackUrlMax: { ro: "other", ru: "many" },
  gracePeriodMaxDays: { ro: "other", ru: "many" },
  gracePeriodMaxMin: { ro: "other", ru: "many" },
  icpcPenaltyMaxMin: { ro: "other", ru: "many" },
  identicalQueryMinChars: { ro: "other", ru: "many" },
  imageMaxEdge: { ro: "other", ru: "many" },
  imageMaxKb: { ro: "few", ru: "many" },
  largePasteChars: { ro: "other", ru: "many" },
  longAbsenceCount: { ro: "few", ru: "many" },
  longAbsenceMinutes: { ro: "few", ru: "many" },
  notesMaxChars: { ro: "other", ru: "many" },
  openQuestionsMax: { ro: "other", ru: "many" },
  passwordMin: { ro: "few", ru: "many" },
  prizePlacesMax: { ro: "other", ru: "many" },
  questionMaxChars: { ro: "other", ru: "many" },
  registrationNameMax: { ro: "other", ru: "many" },
  resultsMaxRows: { ro: "other", ru: "many" },
  signInLockoutMinutes: { ro: "few", ru: "many" },
  signupCodeDigits: { ro: "few", ru: "many" },
  signupLoginMax: { ro: "other", ru: "few" },
  signupLoginMin: { ro: "few", ru: "few" },
  tabTitleMax: { ro: "other", ru: "many" },
};

describe("the bounds a sentence states", () => {
  test.each(Object.entries(BOUNDS))("%s is a whole number in the sentence's unit", (_name, value) => {
    expect(Number.isInteger(value) && value > 0).toBe(true);
  });

  test.each(LOCALES)("are all filled in the %s dictionary", async (locale) => {
    const left = sentences(await getDictionary(locale)).filter(([, sentence]) => boundsIn(sentence).length > 0);

    expect(left).toEqual([]);
  });

  test("are each stated somewhere, so none is left behind when its sentence goes", () => {
    const used = new Set(sentences(en).flatMap(([, sentence]) => boundsIn(sentence)));

    expect([...names].filter((name) => !used.has(name))).toEqual([]);
  });

  // A locale that writes the number out, or drops it, has gone back to a copy.
  test.each(["ro", "ru"] as const)("are the same in %s as in English, sentence by sentence", (locale) => {
    const theirs = new Map(sentences(RAW[locale]));
    const differing = sentences(en)
      .map(([key, sentence]) => [key, boundsIn(sentence).sort(), boundsIn(theirs.get(key) ?? "").sort()] as const)
      .filter(([, ours, others]) => ours.join() !== others.join());

    expect(differing).toEqual([]);
  });

  test.each(["ro", "ru"] as const)("keep the plural category their %s sentences were written for", (locale) => {
    const rules = new Intl.PluralRules(locale);
    const moved = Object.entries(BOUNDS)
      .filter(([name, value]) => rules.select(value) !== WRITTEN_FOR[name as keyof typeof BOUNDS][locale])
      .map(([name, value]) => ({
        bound: name,
        value,
        writtenFor: WRITTEN_FOR[name as keyof typeof BOUNDS][locale],
        now: rules.select(value),
        reword: sentences(RAW[locale])
          .filter(([, sentence]) => boundsIn(sentence).includes(name))
          .map(([key]) => key),
      }));

    expect(moved).toEqual([]);
  });

  test.each(["en", "ro", "ru"] as const)("in %s are each a bound or a name a screen fills", (locale) => {
    const unknown = sentences(RAW[locale]).flatMap(([key, sentence]) =>
      [...sentence.matchAll(/\{(\w+)\}/g)]
        .map((match) => match[1]!)
        .filter((name) => !names.has(name) && !FILLED_AT_RENDER.has(name))
        .map((name) => `${key}: {${name}}`),
    );

    expect(unknown).toEqual([]);
  });

  describe("with a pinned constant moved", () => {
    afterEach(() => {
      vi.doUnmock("@/lib/api/workspace-terms");
      vi.resetModules();
    });

    // Mocked here alone, so every other case reads the real constants.
    test("follow the constant they are filled from, written as each locale writes a number", async () => {
      vi.resetModules();
      vi.doMock("@/lib/api/workspace-terms", async (importOriginal) => ({
        ...(await importOriginal<typeof import("@/lib/api/workspace-terms")>()),
        NOTES_MAX_CHARS: 12345,
      }));
      const { getDictionary: load } = await import("./dictionary");

      expect((await load("en")).errors.workspace_notes_too_long).toContain("12,345 characters");
      expect((await load("ro")).errors.workspace_notes_too_long).toContain("12.345 de caractere");
      expect((await load("ru")).errors.workspace_notes_too_long).toContain("12\u00a0345 символов");
    });
  });

  test("are filled once per locale, the same dictionary for every request", async () => {
    const first = await getDictionary("ro");

    expect(await getDictionary("ro")).toBe(first);
    expect(await getDictionary("en")).not.toBe(first);
  });

  test("leave a placeholder that is not a bound for the screen to fill", async () => {
    expect((await getDictionary("en")).auth.confirm.lede).toContain("{email}");
  });
});
