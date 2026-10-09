import { describe, expect, it } from "vitest";

import {
  myCsvHref,
  myQueriesPath,
  profileContestsSchema,
  profileReportSchema,
  profileSummarySchema,
  profileWorkspaceSchema,
} from "./profile";

const CONTEST = "6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d09";

describe("the profile summary wire shape", () => {
  it("carries the four numbers the header is written from", () => {
    const summary = profileSummarySchema.parse({
      contests: 7,
      finished: 5,
      queries: 412,
      solved: 19,
    });

    expect(summary).toEqual({ contests: 7, finished: 5, queries: 412, solved: 19 });
  });
});

describe("the profile contest list wire shape", () => {
  it("leaves a running contest without a result", () => {
    const list = profileContestsSchema.parse({
      truncated: false,
      items: [
        {
          contest_id: "6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d01",
          title: "The Greenhouse",
          status: "running",
          starts_at: "2026-05-14T07:00:00Z",
          ends_at: "2026-05-14T10:00:00Z",
          registration_status: "active",
          over: false,
        },
      ],
    });

    expect(list.items[0].result).toBeUndefined();
    expect(list.items[0].contestId).toBe("6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d01");
    expect(list.truncated).toBe(false);
  });

  it("carries the penalty of an ICPC result and no penalty anywhere else", () => {
    const list = profileContestsSchema.parse({
      truncated: true,
      items: [
        {
          contest_id: "6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d02",
          title: "Winter ICPC",
          status: "finished",
          starts_at: "2026-02-01T07:00:00Z",
          ends_at: "2026-02-01T11:00:00Z",
          registration_status: "finished",
          over: true,
          result: {
            scoring: "icpc",
            points: 0,
            solved: 4,
            penalty: 87,
            state: "final",
            place_open: true,
          },
        },
        {
          contest_id: "6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d03",
          title: "Autumn points",
          status: "finished",
          registration_status: "finished",
          over: true,
          result: { scoring: "points", points: 60, solved: 3, state: "frozen", place_open: false },
        },
      ],
    });

    expect(list.items[0].result).toMatchObject({ scoring: "icpc", solved: 4, penalty: 87 });
    expect(list.items[1].result?.penalty).toBeUndefined();
    expect(list.truncated).toBe(true);
  });

  it("reads whether the table is open", () => {
    const list = profileContestsSchema.parse({
      truncated: false,
      items: [
        {
          contest_id: "6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d04",
          title: "Frozen still",
          status: "finished",
          registration_status: "finished",
          over: true,
          result: { scoring: "points", points: 12, solved: 1, state: "frozen", place_open: false },
        },
      ],
    });

    expect(list.items[0].result?.placeOpen).toBe(false);
    expect(list.items[0].result?.state).toBe("frozen");
  });

  // Reached by a participant disqualified before the contest started.
  it("reads a table that has not opened yet", () => {
    const list = profileContestsSchema.parse({
      truncated: false,
      items: [
        {
          contest_id: "6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d05",
          title: "Never began",
          status: "published",
          registration_status: "disqualified",
          over: true,
          result: { scoring: "points", points: 0, solved: 0, state: "not_started", place_open: false },
        },
      ],
    });

    expect(list.items[0].result?.state).toBe("not_started");
    expect(list.items[0].result?.placeOpen).toBe(false);
  });
});

describe("the report wire shape", () => {
  const report = {
    contest_id: CONTEST,
    title: "The Greenhouse",
    status: "finished",
    starts_at: "2026-05-14T07:00:00Z",
    ends_at: "2026-05-14T10:00:00Z",
    result: { scoring: "points", points: 60, solved: 3, state: "final", place_open: true, place: 4, participants: 31 },
    started_at: "2026-05-14T07:02:00Z",
    queries: 120,
    successful_queries: 98,
    worked_ms: 5_400_000,
    questions: [
      { question_id: CONTEST, ord: 1, attempts: 2, solved: true, solved_at: "2026-05-14T08:00:00Z", points: 20, penalty: 0 },
      { question_id: CONTEST, ord: 2, attempts: 3, solved: false, points: 0, penalty: 0 },
    ],
  };

  it("carries the place, the count it is a place among, and the work behind it", () => {
    const parsed = profileReportSchema.parse(report);

    expect(parsed.result?.place).toBe(4);
    expect(parsed.result?.participants).toBe(31);
    expect(parsed.workedMs).toBe(5_400_000);
    expect(parsed.successfulQueries).toBe(98);
    expect(parsed.questions[1]).toMatchObject({ ord: 2, attempts: 3, solved: false });
    expect(parsed.questions[1].solvedAt).toBeUndefined();
    expect(parsed.disqualified).toBe(false);
  });

  it("reads no place as no place, whichever of the two reasons it is", () => {
    const frozen = profileReportSchema.parse({
      ...report,
      result: { scoring: "points", points: 60, solved: 3, state: "frozen", place_open: false, place: null, participants: null },
    });
    expect(frozen.result?.placeOpen).toBe(false);
    expect(frozen.result?.place).toBeNull();

    const loser = profileReportSchema.parse({
      ...report,
      result: { scoring: "winner", points: 0, solved: 1, state: "final", place_open: true, place: null, participants: null },
    });
    expect(loser.result?.placeOpen).toBe(true);
    expect(loser.result?.place).toBeNull();
    expect(loser.result?.winner).toBe(false);
  });

  it("reads what a question cost in ICPC minutes, and nought where none was sent", () => {
    const parsed = profileReportSchema.parse({
      ...report,
      result: { scoring: "icpc", points: 0, solved: 1, penalty: 47, state: "final", place_open: true, place: 2, participants: 9 },
      questions: [
        { question_id: CONTEST, ord: 1, attempts: 2, solved: true, solved_at: "2026-05-14T08:00:00Z", points: 0, penalty: 47 },
        { question_id: CONTEST, ord: 2, attempts: 1, solved: false, points: 0 },
      ],
    });

    expect(parsed.questions[0].penalty).toBe(47);
    expect(parsed.questions[1].penalty).toBe(0);
  });

  it("reads a report whose table has not opened yet", () => {
    const parsed = profileReportSchema.parse({
      ...report,
      status: "published",
      disqualified: true,
      result: {
        scoring: "points",
        points: 0,
        solved: 0,
        state: "not_started",
        place_open: false,
        place: null,
        participants: null,
      },
    });

    expect(parsed.result?.state).toBe("not_started");
    expect(parsed.result?.placeOpen).toBe(false);
  });

  // The table is bounded: every participant below the cut has a null result.
  it("reads a report whose row is outside the published table", () => {
    const parsed = profileReportSchema.parse({ ...report, result: null });

    expect(parsed.result).toBeNull();
    expect(parsed.queries).toBe(120);
    expect(parsed.questions).toHaveLength(2);
  });

  it("says a participant was disqualified, and nothing about why", () => {
    const parsed = profileReportSchema.parse({ ...report, disqualified: true });

    expect(parsed.disqualified).toBe(true);
    expect(Object.keys(parsed)).not.toContain("reason");
  });

  it("reads the notes and the tabs as they were left", () => {
    const parsed = profileWorkspaceSchema.parse({
      notes: { body: "suspects: 3", updated_at: "2026-05-14T09:00:00Z" },
      tabs: [{ id: CONTEST, title: "Query 1", position: 0, body: "SELECT 1", updated_at: "2026-05-14T09:10:00Z" }],
    });

    expect(parsed.notes.body).toBe("suspects: 3");
    expect(parsed.tabs[0]).toMatchObject({ title: "Query 1", body: "SELECT 1" });
  });
});

describe("the report's addresses", () => {
  it("asks for its own queries under /me, with the filters given and no others", () => {
    expect(myQueriesPath(CONTEST, { status: "error", q: "guests", cursor: "c1" })).toBe(
      `/me/contests/${CONTEST}/queries?status=error&q=guests&cursor=c1`,
    );
    expect(myQueriesPath(CONTEST, {})).toBe(`/me/contests/${CONTEST}/queries`);
  });

  it("points the download at the API's own file, not at a blob built in the page", () => {
    expect(myCsvHref(CONTEST)).toBe(`/api/v1/me/contests/${CONTEST}/log.csv`);
  });
});
