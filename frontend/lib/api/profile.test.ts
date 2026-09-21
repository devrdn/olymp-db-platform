import { describe, expect, it } from "vitest";

import { profileContestsSchema, profileSummarySchema } from "./profile";

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
  // A contest that has not ended for this participant carries no result at
  // all: during one, the profile shows nothing of what is happening inside
  // it. The row has to be able to tell "no result yet" from "a result of
  // nought", which is why the field is absent rather than zeroed.
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

  // ICPC writes no points at all, so the result of an ICPC contest is its
  // solved count and its penalty minutes. The penalty is the field that says
  // which of the two shapes this is: it is sent in that mode and in no other.
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

  // `place_open` is the one thing the list says about the table, and the row
  // says the place is not there yet when it is false. Read wrongly it would
  // promise a place the report does not have.
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
});
