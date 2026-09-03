import { describe, expect, test } from "vitest";

import { AUDIT_ACTIONS } from "../api/audit-terms";
import { LOCALES } from "./config";
import { getDictionary } from "./dictionary";

describe("getDictionary", () => {
  test("serves the strings of the locale asked for", async () => {
    const en = await getDictionary("en");
    const ru = await getDictionary("ru");

    expect(en.contests.heading).toBe("Contests");
    expect(ru.contests.heading).toBe("Олимпиады");
  });

  test("every locale carries every key, so no screen falls back to English", async () => {
    const dictionaries = await Promise.all(LOCALES.map((locale) => getDictionary(locale)));

    const keysOf = (value: unknown, prefix = ""): string[] =>
      typeof value === "object" && value !== null
        ? Object.entries(value).flatMap(([k, v]) => keysOf(v, prefix ? `${prefix}.${k}` : k))
        : [prefix];

    const [reference, ...rest] = dictionaries.map((d) => keysOf(d).sort());

    for (const [index, keys] of rest.entries()) {
      expect({ locale: LOCALES[index + 1], keys }).toEqual({
        locale: LOCALES[index + 1],
        keys: reference,
      });
    }
  });

  test("every action the server can record has a translation in every locale", async () => {
    // The parity test above only proves the three dictionaries agree with
    // each other — it says nothing about whether they cover the server's
    // vocabulary. An action missing from all three at once, the way
    // `user.delete` and `user.restore` were, would pass that test and still
    // reach the audit screen as a raw code. AUDIT_ACTIONS is the reference
    // this test checks the dictionaries against instead.
    const dictionaries = await Promise.all(LOCALES.map((locale) => getDictionary(locale)));

    for (const dict of dictionaries) {
      const actions = dict.audit.actions as Record<string, string>;
      for (const action of AUDIT_ACTIONS) {
        expect(actions[action], `missing translation for "${action}"`).toBeTypeOf("string");
      }
    }
  });
});
