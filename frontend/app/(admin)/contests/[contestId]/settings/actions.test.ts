import { beforeEach, describe, expect, test, vi } from "vitest";

// `vi.mock` factories are hoisted, so their mocks are built with `vi.hoisted`.
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
 * A running contest's disabled shape fieldset submits nothing; sending defaults
 * instead would make the server's `checkRunningChange` refuse the whole save.
 */
describe("saveSettingsAction, a running contest's locked shape", () => {
  test("sends none of the shape fields, only what the open panels actually carried", async () => {
    serverRequest.mockResolvedValueOnce({});

    // A locked shape submits nothing; the open panels submit as usual.
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
    // The open panels still went through.
    expect(init.body).toMatchObject({ enrollment: "open" });
  });

  test("still refuses an invalid rate or grace on the same locked-shape save", async () => {
    // An absent shape must not skip validation of the rest of the form.
    serverRequest.mockResolvedValueOnce({});
    await saveSettingsAction({}, form({ queryRateLimitPerMin: "-5" }));
    expect(serverRequest).toHaveBeenCalledTimes(1);
    const [, init] = serverRequest.mock.calls[0] as [string, { body: { settings: Record<string, unknown> } }];
    // A negative rate degrades to no limit rather than being sent.
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
