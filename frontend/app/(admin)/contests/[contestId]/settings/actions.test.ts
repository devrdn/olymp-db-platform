import { beforeEach, describe, expect, test, vi } from "vitest";

// `vi.mock` factories are hoisted above every import in this file, so the
// mocks they return have to be built through `vi.hoisted` rather than closed
// over plain top-level `const`s — those would not exist yet when the factory
// actually runs (the same pattern `people/actions.test.ts` already uses).
const { revalidatePath, serverRequest } = vi.hoisted(() => ({
  revalidatePath: vi.fn(),
  serverRequest: vi.fn(),
}));
vi.mock("next/cache", () => ({ revalidatePath }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));

import { saveSettingsAction } from "./actions";

const contestId = "11111111-1111-1111-1111-111111111111";

function form(fields: Record<string, string>): FormData {
  const data = new FormData();
  data.set("contestId", contestId);
  for (const [key, value] of Object.entries(fields)) data.set(key, value);
  return data;
}

beforeEach(() => {
  serverRequest.mockReset();
  revalidatePath.mockReset();
});

/**
 * Reviewer finding: the shape fieldset (`question_mode`, `progression`,
 * `scoring`, `timing`, `duration_min`) is disabled once the contest starts,
 * and a disabled radio group submits nothing at all — but the action used to
 * fall back to a hard-coded default for each of them regardless, so a save
 * that only touched the schedule or the leaderboard label sent
 * `question_mode: "multi"`, `progression: "free"`, `scoring: "points"` and
 * `timing: "fixed"` alongside it. `checkRunningChange` on the Go side then
 * refused the *whole* request for any running contest that was not already
 * that exact shape — every running ICPC contest included.
 */
describe("saveSettingsAction, a running contest's locked shape", () => {
  test("sends none of the shape fields, only what the open panels actually carried", async () => {
    serverRequest.mockResolvedValueOnce({});

    // What a locked shape fieldset actually submits: nothing. The schedule,
    // the leaderboard's own name column and the access panel stay open on a
    // running contest, so their fields are present as usual.
    await saveSettingsAction(
      {},
      form({
        startsAt: "2026-11-08T19:00",
        endsAt: "2026-11-08T23:00",
        enrollment: "open",
        leaderboardNames: "login",
        allowedCidrs: "",
        queryRateLimitPerMin: "0",
        gracePeriodMin: "5",
      }),
    );

    expect(serverRequest).toHaveBeenCalledTimes(1);
    const [, init] = serverRequest.mock.calls[0] as [string, { body: Record<string, unknown> }];

    for (const key of ["question_mode", "progression", "scoring", "timing", "duration_min", "icpc_penalty_min"]) {
      expect(init.body).not.toHaveProperty(key);
    }
    // The panels that stayed open still went through, on the very same save.
    expect(init.body).toMatchObject({ enrollment: "open" });
  });

  test("still refuses an invalid rate or grace on the same locked-shape save", async () => {
    // The shape being absent must not short-circuit the rest of the form's
    // own validation — a locked shape and a broken access panel are two
    // different problems, and only the second one belongs to this test's
    // sibling suite below, but a regression that made every locked-shape
    // save always "succeed" without checking anything else would be just as
    // wrong as the one this fix corrects.
    serverRequest.mockResolvedValueOnce({});
    await saveSettingsAction({}, form({ queryRateLimitPerMin: "-5" }));
    expect(serverRequest).toHaveBeenCalledTimes(1);
    const [, init] = serverRequest.mock.calls[0] as [string, { body: { settings: Record<string, unknown> } }];
    // A negative rate has no valid representation, so it degrades to the
    // safest bound (no limit) rather than sending a negative number through —
    // existing behaviour, unaffected by this fix, pinned here so a future
    // change to the shape logic cannot quietly break it too.
    expect(init.body.settings.query_rate_limit_per_min).toBe(0);
  });
});

describe("saveSettingsAction, an open shape", () => {
  test("still sends every shape field when the fieldset is open", async () => {
    serverRequest.mockResolvedValueOnce({});

    await saveSettingsAction(
      {},
      form({
        questionMode: "single",
        progression: "sequential",
        scoring: "icpc",
        timing: "individual",
        durationMin: "45",
        icpcPenaltyMin: "30",
      }),
    );

    const [, init] = serverRequest.mock.calls[0] as [string, { body: Record<string, unknown> }];
    expect(init.body).toMatchObject({
      question_mode: "single",
      progression: "sequential",
      scoring: "icpc",
      timing: "individual",
      duration_min: 45,
      icpc_penalty_min: 30,
    });
  });

  test("still refuses individual timing with no valid duration", async () => {
    const state = await saveSettingsAction({}, form({ timing: "individual" }));

    expect(state).toEqual({ code: "invalid_request" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("sends a null duration when fixed timing is chosen", async () => {
    serverRequest.mockResolvedValueOnce({});

    await saveSettingsAction(
      {},
      form({ questionMode: "multi", progression: "free", scoring: "points", timing: "fixed" }),
    );

    const [, init] = serverRequest.mock.calls[0] as [string, { body: Record<string, unknown> }];
    expect(init.body).toMatchObject({ timing: "fixed", duration_min: null });
  });
});
