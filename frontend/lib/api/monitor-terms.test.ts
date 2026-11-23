import { describe, expect, test } from "vitest";

import { monitorCsvHref, participantCsvHref } from "./monitor-terms";

const CONTEST = "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";
const REG = "9a1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";

describe("the CSV addresses", () => {
  test("offers the contest's CSV as an API path on this origin", () => {
    expect(monitorCsvHref(CONTEST)).toBe(`/api/v1/contests/${CONTEST}/monitor/export.csv`);
  });

  test("offers the participant's CSV as an API path on this origin", () => {
    expect(participantCsvHref(CONTEST, REG)).toBe(`/api/v1/contests/${CONTEST}/monitor/participants/${REG}/export.csv`);
  });
});
