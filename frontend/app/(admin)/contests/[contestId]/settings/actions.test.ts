import { beforeEach, describe, expect, test, vi } from "vitest";

// `vi.mock` factories are hoisted, so their mocks are built with `vi.hoisted`.
const { redirect, revalidatePath, serverRequest } = vi.hoisted(() => ({
  redirect: vi.fn(),
  revalidatePath: vi.fn(),
  serverRequest: vi.fn(),
}));
vi.mock("next/cache", () => ({ revalidatePath }));
vi.mock("next/navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("next/navigation")>()),
  redirect,
}));
vi.mock("@/lib/api/server", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/server")>()),
  serverRequest,
}));

import { ApiError } from "@/lib/api/client";
import {
  deleteDraftAction,
  extendGraceAction,
  saveArchiveSettingsAction,
  saveLanguagesAction,
  savePrizePlacesAction,
  saveSettingsAction,
} from "./actions";

const contestId = "11111111-1111-1111-1111-111111111111";

function form(fields: Record<string, string>): FormData {
  const data = new FormData();
  data.set("contestId", contestId);
  for (const [key, value] of Object.entries(fields)) data.set(key, value);
  return data;
}

/** What a form rendered at contest version 7 submits. */
function renderedForm(fields: Record<string, string>): FormData {
  return form({ expectedVersion: "7", ...fields });
}

beforeEach(() => {
  serverRequest.mockReset();
  revalidatePath.mockReset();
  redirect.mockReset();
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
      renderedForm({
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
    await saveSettingsAction({}, renderedForm({ queryRateLimitPerMin: "-5" }));
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
      renderedForm({
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
    const state = await saveSettingsAction({}, renderedForm({ timing: "individual" }));

    expect(state).toEqual({ code: "invalid_request" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("sends a null duration when fixed timing is chosen", async () => {
    serverRequest.mockResolvedValueOnce({});

    await saveSettingsAction(
      {},
      renderedForm({ questionMode: "multi", progression: "free", scoring: "points", timing: "fixed" }),
    );

    const [, init] = serverRequest.mock.calls[0] as [string, { body: Record<string, unknown> }];
    expect(init.body).toMatchObject({ timing: "fixed", duration_min: null });
  });
});

describe("saveSettingsAction, the leaderboard's names", () => {
  test("keeps the choice the form carried", async () => {
    serverRequest.mockResolvedValueOnce({});

    await saveSettingsAction({}, renderedForm({ leaderboardNames: "login" }));

    const [, init] = serverRequest.mock.calls[0] as [string, { body: { leaderboard: { names: string } } }];
    expect(init.body.leaderboard.names).toBe("login");
  });

  // The table is public and a self-registered login is an email address.
  test("shows full names when the form carries no readable choice", async () => {
    serverRequest.mockResolvedValueOnce({});

    await saveSettingsAction({}, renderedForm({ leaderboardNames: "nickname" }));

    const [, init] = serverRequest.mock.calls[0] as [string, { body: { leaderboard: { names: string } } }];
    expect(init.body.leaderboard.names).toBe("full_name");
  });
});

/** The three saves of the contest row, each with the least its form submits besides the version. */
const versionGuardedSaves = [
  ["saveSettingsAction", saveSettingsAction, "", "PATCH", {}],
  ["savePrizePlacesAction", savePrizePlacesAction, "/prize-places", "PUT", { prizeFirst: "1" }],
  ["saveArchiveSettingsAction", saveArchiveSettingsAction, "/archive-settings", "PUT", { visibility: "open" }],
] as const;

describe.each(versionGuardedSaves)("%s, the version the form was loaded at", (_name, action, suffix, method, fields) => {
  test("sends it back, so a save made after the contest moved is refused", async () => {
    serverRequest.mockResolvedValueOnce({});

    await action({}, renderedForm({ ...fields, expectedVersion: "12" }));

    const [path, init] = serverRequest.mock.calls[0] as [string, { method: string; body: Record<string, unknown> }];
    expect(path).toBe(`/contests/${contestId}${suffix}`);
    expect(init.method).toBe(method);
    expect(init.body.expected_version).toBe(12);
  });

  // The column's default: a contest nothing has written since its creation.
  test("sends a never-written contest's version, zero", async () => {
    serverRequest.mockResolvedValueOnce({});

    await action({}, renderedForm({ ...fields, expectedVersion: "0" }));

    const [, init] = serverRequest.mock.calls[0] as [string, { body: Record<string, unknown> }];
    expect(init.body.expected_version).toBe(0);
  });

  // All three saves move the one version the other forms on the page send.
  test("re-reads the workspace once saved, so every form on the page sends the moved version", async () => {
    serverRequest.mockResolvedValueOnce({});

    expect(await action({}, renderedForm(fields))).toEqual({ ok: true });

    expect(revalidatePath).toHaveBeenCalledWith(`/contests/${contestId}`, "layout");
  });

  test.each([
    ["absent", undefined],
    ["blank", ""],
    ["not a number", "seven"],
    ["fractional", "7.5"],
    ["negative", "-7"],
    ["signed", "+7"],
    ["past what a number holds exactly", "9007199254740993"],
  ])("refuses a form whose version is %s without asking the server", async (_, version) => {
    const data = renderedForm(fields);
    if (version === undefined) data.delete("expectedVersion");
    else data.set("expectedVersion", version);

    expect(await action({}, data)).toEqual({ code: "invalid_request" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("hands back the server's refusal of a contest that moved since", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("contest_changed", 409, "changed"));

    expect(await action({}, renderedForm(fields))).toEqual({ code: "contest_changed" });
    expect(revalidatePath).not.toHaveBeenCalled();
  });
});

describe("saveLanguagesAction", () => {
  // The language set moves the contest's version, and the settings, prize
  // places and archive forms on the same page send the version back: they
  // must read the new one.
  test("re-reads the contest's pages, the settings form's version among them", async () => {
    serverRequest.mockResolvedValueOnce(undefined);
    const data = form({ defaultLanguage: "ro" });
    data.append("languages", "en");
    data.append("languages", "ro");

    expect(await saveLanguagesAction({}, data)).toEqual({ ok: true });

    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/languages`, {
      method: "PUT",
      body: {
        languages: [
          { code: "en", is_default: false },
          { code: "ro", is_default: true },
        ],
      },
    });
    expect(revalidatePath).toHaveBeenCalledWith(`/contests/${contestId}`, "layout");
  });
});

describe("saveSettingsAction, the result download", () => {
  test("offers the CSV file when the box is ticked", async () => {
    serverRequest.mockResolvedValueOnce({});

    await saveSettingsAction({}, renderedForm({ resultDownload: "on" }));

    const [, init] = serverRequest.mock.calls[0] as [string, { body: Record<string, unknown> }];
    expect(init.body.result_download).toBe(true);
  });

  test("withdraws it when the box is cleared", async () => {
    serverRequest.mockResolvedValueOnce({});

    await saveSettingsAction({}, renderedForm({}));

    const [, init] = serverRequest.mock.calls[0] as [string, { body: Record<string, unknown> }];
    expect(init.body.result_download).toBe(false);
  });
});

describe("saveSettingsAction, the schema diagram", () => {
  test("offers the diagram when the box is ticked", async () => {
    serverRequest.mockResolvedValueOnce({});

    await saveSettingsAction({}, renderedForm({ schemaDiagram: "on" }));

    const [, init] = serverRequest.mock.calls[0] as [string, { body: Record<string, unknown> }];
    expect(init.body.schema_diagram).toBe(true);
  });

  test("withdraws it when the box is cleared", async () => {
    serverRequest.mockResolvedValueOnce({});

    await saveSettingsAction({}, renderedForm({}));

    const [, init] = serverRequest.mock.calls[0] as [string, { body: Record<string, unknown> }];
    expect(init.body.schema_diagram).toBe(false);
  });
});

describe("savePrizePlacesAction", () => {
  test("replaces the three counts on their own endpoint, a blank one as none", async () => {
    serverRequest.mockResolvedValueOnce({});

    const state = await savePrizePlacesAction({}, renderedForm({ prizeFirst: "1", prizeSecond: "2", prizeThird: "" }));

    expect(state).toEqual({ ok: true });
    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/prize-places`, {
      method: "PUT",
      body: { first: 1, second: 2, third: 0, expected_version: 7 },
    });
  });

  test("refuses a count past the bound before asking the server", async () => {
    const state = await savePrizePlacesAction({}, renderedForm({ prizeFirst: "1001" }));

    expect(state).toEqual({ code: "invalid_request" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("refuses a contest id that is not an identifier", async () => {
    const data = form({ prizeFirst: "1" });
    data.set("contestId", "../users");

    expect(await savePrizePlacesAction({}, data)).toEqual({ code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("hands back the server's refusal, such as an archived contest", async () => {
    const { ApiError } = await import("@/lib/api/client");
    serverRequest.mockRejectedValueOnce(new ApiError("not_editable", 409, "archived"));

    expect(await savePrizePlacesAction({}, renderedForm({ prizeFirst: "1" }))).toEqual({ code: "not_editable" });
  });
});

/**
 * The request replaces the whole `settings` object, and the API cannot tell a
 * missing `trusted_domain_only` from false: every save must carry it.
 */
describe("saveSettingsAction, the trusted-domain limit", () => {
  test("sends it on when the form carries it", async () => {
    serverRequest.mockResolvedValueOnce({});

    await saveSettingsAction({}, renderedForm({ queryRateLimitPerMin: "10", trustedDomainOnly: "on" }));

    const [, init] = serverRequest.mock.calls[0] as [string, { body: { settings: Record<string, unknown> } }];
    expect(init.body.settings.trusted_domain_only).toBe(true);
  });

  test("sends it off, explicitly, when the form does not", async () => {
    serverRequest.mockResolvedValueOnce({});

    await saveSettingsAction({}, renderedForm({ queryRateLimitPerMin: "10" }));

    const [, init] = serverRequest.mock.calls[0] as [string, { body: { settings: Record<string, unknown> } }];
    expect(init.body.settings).toHaveProperty("trusted_domain_only", false);
  });

  test("never sends a forged contest id", async () => {
    const data = form({ trustedDomainOnly: "on" });
    data.set("contestId", "../users");

    expect(await saveSettingsAction({}, data)).toEqual({ code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });
});

describe("saveArchiveSettingsAction", () => {
  test("replaces both choices on their own endpoint", async () => {
    serverRequest.mockResolvedValueOnce({});

    const state = await saveArchiveSettingsAction({}, renderedForm({ visibility: "closed", archiveTasks: "on" }));

    expect(state).toEqual({ ok: true });
    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/archive-settings`, {
      method: "PUT",
      body: { visibility: "closed", archive_tasks: true, expected_version: 7 },
    });
  });

  test("sends the tasks off when the box is absent", async () => {
    serverRequest.mockResolvedValueOnce({});

    await saveArchiveSettingsAction({}, renderedForm({ visibility: "open" }));

    const [, init] = serverRequest.mock.calls[0] as [string, { body: Record<string, unknown> }];
    expect(init.body).toEqual({ visibility: "open", archive_tasks: false, expected_version: 7 });
  });

  test("refuses a visibility outside the list before asking the server", async () => {
    expect(await saveArchiveSettingsAction({}, renderedForm({ visibility: "public" }))).toEqual({ code: "invalid_request" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("refuses a contest id that is not an identifier", async () => {
    const data = form({ visibility: "open" });
    data.set("contestId", "../users");

    expect(await saveArchiveSettingsAction({}, data)).toEqual({ code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("hands back the server's refusal to close a self-registration contest", async () => {
    const { ApiError } = await import("@/lib/api/client");
    serverRequest.mockRejectedValueOnce(new ApiError("visibility_needs_invite_only", 409, "open enrolment"));

    expect(await saveArchiveSettingsAction({}, renderedForm({ visibility: "closed" }))).toEqual({
      code: "visibility_needs_invite_only",
    });
  });
});

describe("extendGraceAction", () => {
  test("extends the grace on its own endpoint", async () => {
    serverRequest.mockResolvedValueOnce({});

    const state = await extendGraceAction({}, form({ gracePeriodMin: "4320", currentGraceMin: "1440" }));

    expect(state).toEqual({ ok: true });
    expect(serverRequest).toHaveBeenCalledTimes(1);
    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/grace`, {
      method: "PATCH",
      body: { grace_period_min: 4320 },
    });
    expect(revalidatePath).toHaveBeenCalled();
  });

  test("hands back the server's refusal to shorten", async () => {
    const { ApiError } = await import("@/lib/api/client");
    serverRequest.mockRejectedValueOnce(new ApiError("grace_not_longer", 400, "not longer"));

    expect(await extendGraceAction({}, form({ gracePeriodMin: "60", currentGraceMin: "0" }))).toEqual({
      code: "grace_not_longer",
    });
  });

  test("refuses a value no longer than the contest's own grace without asking the server", async () => {
    const state = await extendGraceAction({}, form({ gracePeriodMin: "100", currentGraceMin: "1440" }));

    expect(state).toEqual({ code: "grace_not_longer" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("refuses a value past the bound without asking the server", async () => {
    const state = await extendGraceAction({}, form({ gracePeriodMin: "129601", currentGraceMin: "0" }));

    expect(state).toEqual({ code: "invalid_request" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("refuses a contest id that is not an identifier", async () => {
    const data = form({ gracePeriodMin: "4320", currentGraceMin: "0" });
    data.set("contestId", "../users");

    expect(await extendGraceAction({}, data)).toEqual({ code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });
});

describe("deleteDraftAction", () => {
  test("deletes the draft, refreshes the register and goes there", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    await deleteDraftAction({}, form({}));

    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}`, { method: "DELETE" });
    expect(revalidatePath).toHaveBeenCalledWith("/contests");
    expect(redirect).toHaveBeenCalledWith("/contests");
  });

  test.each(["build_in_progress", "game_in_use", "not_editable"])(
    "answers %s and stays on the settings",
    async (code) => {
      serverRequest.mockRejectedValueOnce(new ApiError(code, 409, "refused"));

      expect(await deleteDraftAction({}, form({}))).toEqual({ code });
      expect(redirect).not.toHaveBeenCalled();
      expect(revalidatePath).not.toHaveBeenCalled();
    },
  );

  test("refuses an id that is not one without a request", async () => {
    const data = new FormData();
    data.set("contestId", "../x");

    expect(await deleteDraftAction({}, data)).toEqual({ code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });
});
