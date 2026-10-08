import { readFileSync } from "node:fs";

import { describe, expect, test } from "vitest";

import { getDictionary } from "@/lib/i18n/dictionary";

import { NAMED_CODES, isClosed, refusalKind, showsReference } from "./refusals";

/** The codes the API declares, read from the contract it generates. */
function contractCodes(): Set<string> {
  const path = "../docs/api/error-codes.json";
  let contract: { codes?: { code: string }[] };
  try {
    contract = JSON.parse(readFileSync(path, "utf8"));
  } catch (cause) {
    throw new Error(`Cannot read ${path}. Regenerate it with \`make api-contract\`.`, { cause });
  }
  return new Set((contract.codes ?? []).map((entry) => entry.code));
}

describe("refusalKind", () => {
  test.each([
    ["contest_finished", "closed"],
    ["contest_ended", "closed"],
    ["deadline_passed", "closed"],
    ["contest_not_running", "dormant"],
    ["not_a_participant", "excluded"],
    ["address_not_allowed", "elsewhere"],
    ["query_too_often", "passing"],
    ["query_busy", "passing"],
    ["query_already_running", "passing"],
    ["no_game_yet", "passing"],
    ["answer_too_often", "passing"],
    ["attempt_conflict", "passing"],
    ["internal_error", "fault"],
    ["query_service_down", "fault"],
    ["game_cluster_full", "fault"],
    ["unreachable", "fault"],
    ["query_database_error", "refused"],
    ["answer_too_long", "refused"],
  ])("%s is %s", (code, kind) => {
    expect(refusalKind(code)).toBe(kind);
  });

  // Most of the vocabulary is this: the request itself was refused, and what
  // to change is in the sentence the dictionary holds for its code.
  test("any code it does not name is an ordinary refusal", () => {
    expect(refusalKind("something_new")).toBe("refused");
  });

  // A server code renamed or removed would otherwise leave its name here,
  // and the screen would quietly stop recognising it.
  test("every code it names is one the API declares, or the one the client invents", () => {
    const declared = contractCodes();
    const unknown = NAMED_CODES.filter(
      (code) => code !== "unreachable" && !declared.has(code),
    );
    expect(unknown).toEqual([]);
  });
});

describe("isClosed", () => {
  test("is true only for the contest being over for this participant", () => {
    expect(isClosed("contest_finished")).toBe(true);
    expect(isClosed("contest_ended")).toBe(true);
    expect(isClosed("deadline_passed")).toBe(true);
    // Not open now is not over: a published contest taken back to draft may
    // be published again, and the participant waiting for it must not be
    // told it has finished.
    expect(isClosed("contest_not_running")).toBe(false);
    expect(isClosed("not_a_participant")).toBe(false);
    expect(isClosed("query_too_often")).toBe(false);
  });
});

describe("showsReference", () => {
  test("only a fault, or a code this build has no sentence for, carries a reference", async () => {
    const { errors } = await getDictionary("en");
    expect(showsReference("internal_error", errors)).toBe(true);
    expect(showsReference("answer_too_long", errors)).toBe(false);
    expect(showsReference("query_too_often", errors)).toBe(false);
    expect(showsReference("something_new", errors)).toBe(true);
    // A name every object answers to is still not a sentence the dictionary holds.
    expect(showsReference("constructor", errors)).toBe(true);
  });
});
