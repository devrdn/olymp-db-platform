import { readFileSync } from "node:fs";

import { describe, expect, test } from "vitest";

import { LOGIN_FAILURE_REASONS } from "../api/audit";
import { PUBLISH_PROBLEMS } from "../api/publish-gate";
import { LOCALES } from "./config";
import { getDictionary } from "./dictionary";

/**
 * The audit actions from the generated contract (`docs/api/audit-actions.json`,
 * from `audit.Actions()`), never a hand-typed copy that could drift from it.
 */
function contractActions(): string[] {
  const path = "../docs/api/audit-actions.json";
  let contract: { actions?: unknown };
  try {
    contract = JSON.parse(readFileSync(path, "utf8"));
  } catch (cause) {
    throw new Error(`Cannot read ${path}. Regenerate it with \`make audit-contract\`.`, { cause });
  }
  if (!Array.isArray(contract.actions) || contract.actions.length === 0) {
    throw new Error(`${path} lists no actions; regenerate it with \`make audit-contract\`.`);
  }
  return contract.actions as string[];
}

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
    // Parity alone would pass an action missing from every locale at once.
    const actions = contractActions();
    const dictionaries = await Promise.all(LOCALES.map((locale) => getDictionary(locale)));

    for (const dict of dictionaries) {
      const translated = dict.audit.actions as Record<string, string>;
      for (const action of actions) {
        expect(translated[action], `missing translation for "${action}"`).toBeTypeOf("string");
      }
    }
  });

  test("every publish-gate problem code has a translation in every locale", async () => {
    // No generated contract publishes these codes yet, so PUBLISH_PROBLEMS is
    // the reference.
    const codes = Object.values(PUBLISH_PROBLEMS);
    const dictionaries = await Promise.all(LOCALES.map((locale) => getDictionary(locale)));

    for (const dict of dictionaries) {
      const translated = dict.workspace.gate.problems as Record<string, string>;
      for (const code of codes) {
        expect(translated[code], `missing translation for "${code}"`).toBeTypeOf("string");
      }
    }
  });

  test("every login-failure reason has a translation in every locale", async () => {
    // No generated contract here either; LOGIN_FAILURE_REASONS is the reference.
    const codes = Object.values(LOGIN_FAILURE_REASONS);
    const dictionaries = await Promise.all(LOCALES.map((locale) => getDictionary(locale)));

    for (const dict of dictionaries) {
      const translated = dict.audit.failureReasons as Record<string, string>;
      for (const code of codes) {
        expect(translated[code], `missing translation for "${code}"`).toBeTypeOf("string");
      }
    }
  });
});
