import { describe, expect, test } from "vitest";

import { resultsReportSchema } from "./results";

const contest = {
  id: "7d6b6a58-1c5c-4a41-9f0f-2f2f6f2a0a01",
  titles: { en: "The Library Murder" },
  default_language: "en",
  status: "running",
  icpc_penalty_min: 20,
  prize_places: { first: 1, second: 2, third: 3 },
  starts_at: "2026-11-02T10:00:00Z",
};

describe("resultsReportSchema", () => {
  test("reads an ICPC detailed report and keeps unplaced and disqualified rows apart", () => {
    const wire = {
      contest: { ...contest, scoring: "icpc" },
      status: "preliminary",
      hidden_by_freeze: false,
      generated_at: "2026-11-02T12:00:00Z",
      registered: 2,
      detail: "detailed",
      fields: ["full_name", "login"],
      bonus_credit: false,
      questions: [
        { position: 1, letter: "A", bonus: false, titles: { en: "Who" } },
        { position: 2, letter: "B", bonus: false, titles: { en: "Where" } },
      ],
      rows: [
        {
          registration_id: "a1",
          place: 1,
          prize: 1,
          full_name: "Ana Pop",
          login: "student",
          disqualified: false,
          solved: 1,
          penalty: 32,
          points: null,
          last_solved_at: "2026-11-02T10:12:00Z",
          bonus_credit: null,
          cells: [
            { solved: true, minute: 12, wrong: 1 },
            { solved: false, wrong: 0 },
          ],
        },
        {
          registration_id: "b2",
          place: null,
          prize: null,
          full_name: "Ion Rus",
          login: "runner",
          deleted: true,
          disqualified: true,
          solved: 0,
          penalty: 0,
          points: null,
          last_solved_at: null,
          bonus_credit: null,
          cells: [
            { solved: false, wrong: 0 },
            { solved: false, wrong: 1 },
          ],
        },
      ],
      fingerprint: "0a1b2c3d",
    };

    const report = resultsReportSchema.parse(wire);

    expect(report.contest).toMatchObject({
      scoring: "icpc",
      icpcPenaltyMin: 20,
      defaultLanguage: "en",
      prizePlaces: { first: 1, second: 2, third: 3 },
      startsAt: "2026-11-02T10:00:00Z",
    });
    expect(report.contest.endsAt).toBeUndefined();
    expect(report.questions[0]).toEqual({ position: 1, letter: "A", bonus: false, titles: { en: "Who" } });
    expect(report.mayExportEmail).toBe(false);

    const [placed, unplaced] = report.rows;
    expect(placed).toMatchObject({
      id: "a1",
      place: 1,
      prize: 1,
      fullName: "Ana Pop",
      login: "student",
      deleted: false,
      disqualified: false,
      winner: false,
      penalty: 32,
      points: null,
      lastSolvedAt: "2026-11-02T10:12:00Z",
      bonusCredit: null,
    });
    expect(placed.email).toBeUndefined();
    expect(placed.cells[0]).toEqual({ solved: true, minute: 12, wrong: 1 });
    expect(placed.cells[1].minute).toBeUndefined();
    expect(unplaced).toMatchObject({ place: null, prize: null, deleted: true, disqualified: true, lastSolvedAt: null });
  });

  test("reads a points detailed report with a bonus question whose cell has no points", () => {
    const wire = {
      contest: { ...contest, scoring: "points", icpc_penalty_min: 0, prize_places: { first: 0, second: 0, third: 0 } },
      status: "final",
      hidden_by_freeze: true,
      generated_at: "2026-11-02T12:00:00Z",
      registered: 1,
      detail: "detailed",
      fields: ["login", "email"],
      bonus_credit: true,
      questions: [
        { position: 1, bonus: false, titles: { en: "Who" } },
        { position: 2, bonus: true, titles: { en: "Extra" } },
      ],
      rows: [
        {
          registration_id: "a1",
          place: 1,
          prize: null,
          login: "student",
          email: "ana@example.org",
          disqualified: false,
          winner: true,
          solved: 2,
          penalty: null,
          points: 10,
          last_solved_at: "2026-11-02T10:12:00Z",
          bonus_credit: 3,
          cells: [
            { solved: true, points: 10, attempts: 2 },
            { solved: true, attempts: 1 },
          ],
        },
      ],
      fingerprint: "0a1b2c3d",
    };

    const report = resultsReportSchema.parse(wire);

    expect(report.status).toBe("final");
    expect(report.hiddenByFreeze).toBe(true);
    expect(report.bonusCredit).toBe(true);
    expect(report.questions[1]).toMatchObject({ bonus: true });
    expect(report.questions[0].letter).toBeUndefined();
    const [row] = report.rows;
    expect(row).toMatchObject({ winner: true, penalty: null, points: 10, bonusCredit: 3, email: "ana@example.org" });
    expect(row.fullName).toBeUndefined();
    expect(row.cells).toEqual([
      { solved: true, points: 10, attempts: 2 },
      { solved: true, attempts: 1 },
    ]);
    expect(row.cells[1].points).toBeUndefined();
  });

  test("reads the preview, where may_export_email defaults to false and cells to []", () => {
    const wire = {
      contest: { ...contest, scoring: "icpc" },
      status: "preliminary",
      hidden_by_freeze: false,
      generated_at: "2026-11-02T12:00:00Z",
      registered: 1,
      detail: "summary",
      fields: ["full_name", "login"],
      bonus_credit: false,
      questions: [],
      rows: [
        {
          registration_id: "a1",
          place: 1,
          prize: null,
          full_name: "Ana Pop",
          login: "student",
          disqualified: false,
          solved: 0,
          penalty: 0,
          points: null,
          last_solved_at: null,
          bonus_credit: null,
        },
      ],
      fingerprint: "0a1b2c3d",
    };

    const withoutFlag = resultsReportSchema.parse(wire);
    expect(withoutFlag.mayExportEmail).toBe(false);
    expect(withoutFlag.questions).toEqual([]);
    expect(withoutFlag.rows[0].cells).toEqual([]);

    expect(resultsReportSchema.parse({ ...wire, may_export_email: true }).mayExportEmail).toBe(true);
  });

  test("refuses a status outside preliminary, final and archived", () => {
    const wire = {
      contest: { ...contest, scoring: "icpc" },
      status: "running",
      hidden_by_freeze: false,
      generated_at: "2026-11-02T12:00:00Z",
      registered: 0,
      detail: "summary",
      fields: [],
      bonus_credit: false,
      questions: [],
      rows: [],
      fingerprint: "0a1b2c3d",
    };

    expect(resultsReportSchema.safeParse(wire).success).toBe(false);
    expect(resultsReportSchema.safeParse({ ...wire, status: "archived" }).success).toBe(true);
  });
});
