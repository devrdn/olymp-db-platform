import { describe, expect, test } from "vitest";

import { importResultSchema, parseLogins, participantSchema, removable } from "./people";

const participant = (status: string) =>
  participantSchema.parse({
    registration_id: "1f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6",
    user_id: "2f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6",
    login: "st12345",
    full_name: "A. Student",
    status,
    total_score: 0,
  });

describe("participantSchema", () => {
  test("parses a participant part-way through a contest", () => {
    const parsed = participantSchema.parse({
      registration_id: "1f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6",
      user_id: "2f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6",
      login: "st12345",
      full_name: "A. Student",
      status: "active",
      started_at: "2026-11-08T19:04:00Z",
      total_score: 12,
    });

    expect(parsed).toMatchObject({ login: "st12345", status: "active", totalScore: 12 });
    expect(parsed.finishedAt).toBeUndefined();
  });
});

/**
 * Removing and disqualifying are different acts. Somebody who has started
 * cannot be deleted — their queries and answers are part of the record of the
 * contest — so excluding them keeps everything they did.
 */
describe("removable", () => {
  test("is true only before a participant has started", () => {
    expect(removable(participant("registered"))).toBe(true);
    expect(removable(participant("active"))).toBe(false);
    expect(removable(participant("finished"))).toBe(false);
    expect(removable(participant("disqualified"))).toBe(false);
  });
});

describe("parseLogins", () => {
  test("takes a column pasted out of a spreadsheet", () => {
    expect(parseLogins("st12345\nst12346\nst12347")).toEqual(["st12345", "st12346", "st12347"]);
  });

  test("takes commas, semicolons and stray spaces too", () => {
    expect(parseLogins("st12345, st12346;st12347  st12348")).toEqual([
      "st12345",
      "st12346",
      "st12347",
      "st12348",
    ]);
  });

  /**
   * A name pasted twice is not a failure to report. Sent as two lines, the API
   * skips the second as `already_enrolled`, and the author is shown a problem
   * they did not create.
   */
  test("sends one copy of a login pasted twice", () => {
    expect(parseLogins("st12345\nst12345")).toEqual(["st12345"]);
  });

  test("makes nothing out of nothing", () => {
    expect(parseLogins("  \n\n ")).toEqual([]);
  });
});

describe("importResultSchema", () => {
  test("keeps every skipped line with the reason it was skipped", () => {
    // A partial success is the honest answer: one typo in three hundred rows
    // must not reject the other two hundred and ninety-nine.
    const parsed = importResultSchema.parse({
      added: 298,
      skipped: [
        { ref: "st99999", reason: "unknown_account" },
        { ref: "st12345", reason: "already_enrolled" },
      ],
    });

    expect(parsed.added).toBe(298);
    expect(parsed.skipped).toHaveLength(2);
    expect(parsed.skipped[0]).toEqual({ ref: "st99999", reason: "unknown_account" });
  });
});
