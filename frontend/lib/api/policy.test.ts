import { describe, expect, test } from "vitest";

import { isTableName, parseTables, sqlPolicySchema } from "./policy";

describe("sqlPolicySchema", () => {
  test("parses the policy the API returns", () => {
    const parsed = sqlPolicySchema.parse({
      mode: "read_write",
      writable_tables: ["public.notes"],
      allow_create_view: true,
      allow_own_tables: false,
      allow_temp_tables: true,
      allow_catalog: false,
      disk_quota_ratio: 2,
    });

    expect(parsed).toMatchObject({
      mode: "read_write",
      writableTables: ["public.notes"],
      allowCreateView: true,
      diskQuotaRatio: 2,
    });
  });
});

/**
 * These names become GRANT statements when a participant's database is built,
 * where they cannot be passed as parameters. The narrow form is the thing that
 * makes that construction safe, so it is checked on both sides of the wire.
 */
describe("isTableName", () => {
  test("accepts a plain and a schema-qualified identifier", () => {
    expect(isTableName("suspects")).toBe(true);
    expect(isTableName("public.suspects")).toBe(true);
    expect(isTableName("_evidence_2")).toBe(true);
  });

  test("refuses anything that could end a statement or start another", () => {
    for (const hostile of [
      "suspects; DROP TABLE users",
      'suspects" --',
      "suspects, users",
      "public.suspects.extra",
      "suspects()",
      "*",
    ]) {
      expect(isTableName(hostile)).toBe(false);
    }
  });

  test("refuses an uppercase name, which PostgreSQL would fold anyway", () => {
    expect(isTableName("Suspects")).toBe(false);
  });

  test("refuses a name starting with a digit", () => {
    expect(isTableName("2suspects")).toBe(false);
  });
});

describe("parseTables", () => {
  test("splits the separators a spreadsheet column arrives with", () => {
    expect(parseTables("suspects, evidence\npublic.notes;alibis").tables).toEqual([
      "suspects",
      "evidence",
      "public.notes",
      "alibis",
    ]);
  });

  test("reports every unusable entry rather than stopping at the first", () => {
    // Same reason the publish gate returns all its problems at once: an author
    // fixing one typo per round trip makes as many round trips as they have
    // typos.
    const { tables, rejected } = parseTables("suspects, Bad Name, evidence, 2wrong");

    expect(tables).toEqual(["suspects", "evidence"]);
    expect(rejected).toEqual(["Bad", "Name", "2wrong"]);
  });

  test("keeps one copy of a name pasted twice", () => {
    expect(parseTables("suspects suspects").tables).toEqual(["suspects"]);
  });

  test("makes nothing out of nothing", () => {
    expect(parseTables("   \n  ")).toEqual({ tables: [], rejected: [] });
  });
});
