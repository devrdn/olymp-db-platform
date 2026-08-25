import { describe, expect, test } from "vitest";

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
});
