import { readFileSync } from "node:fs";

import { describe, expect, test } from "vitest";

import { AUDIT_ACTIONS } from "../api/audit-terms";
import { LOCALES } from "./config";
import { getDictionary } from "./dictionary";

/**
 * The audit action vocabulary, read from the contract the backend generates
 * (`docs/api/audit-actions.json`, from `audit.Actions()` — see
 * `backend/cmd/auditcontract`) rather than from `AUDIT_ACTIONS` itself.
 *
 * `AUDIT_ACTIONS` used to be what the coverage test below checked the
 * dictionaries against, and being hand-typed, it was a second copy of the
 * server's vocabulary that nothing tied to the first: an action added to
 * `audit.go` and to `Actions()`, and never copied here, satisfied the Go
 * guard (which reads the same source it is proving) and this test (which
 * checked the dictionaries against this file's own list) at once. Reading
 * the generated contract instead makes that impossible — this file is
 * exact by construction, the same way `frontend/scripts/error-codes.mjs`
 * reads `docs/api/error-codes.json` rather than a copy of the server's error
 * codes. A Go-only addition now fails `go test ./...`
 * (`cmd/auditcontract`'s own `TestTheCommittedContractIsCurrent`) before it
 * can ever reach this file un-regenerated.
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
    // The parity test above only proves the three dictionaries agree with
    // each other — it says nothing about whether they cover the server's
    // vocabulary. An action missing from all three at once, the way
    // `user.delete` and `user.restore` were, would pass that test and still
    // reach the audit screen as a raw code. The generated contract — not
    // AUDIT_ACTIONS, and not a copy of it — is the reference this test
    // checks the dictionaries against.
    const actions = contractActions();
    const dictionaries = await Promise.all(LOCALES.map((locale) => getDictionary(locale)));

    for (const dict of dictionaries) {
      const translated = dict.audit.actions as Record<string, string>;
      for (const action of actions) {
        expect(translated[action], `missing translation for "${action}"`).toBeTypeOf("string");
      }
    }
  });

  test("AUDIT_ACTIONS mirrors the contract exactly", () => {
    // AUDIT_ACTIONS itself is no longer what the coverage test above checks
    // the dictionaries against — the contract is — but it is still read by
    // whoever wants the vocabulary without a server running (see its own
    // doc comment in audit-terms.ts), and a copy nothing checks is a copy
    // that lies eventually. This is what keeps it honest.
    expect([...AUDIT_ACTIONS].sort()).toEqual([...contractActions()].sort());
  });
});
