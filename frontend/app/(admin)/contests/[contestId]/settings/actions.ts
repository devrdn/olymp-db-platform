"use server";

import { revalidatePath } from "next/cache";

import { ApiError } from "@/lib/api/client";
import {
  contestCoverSchema,
  ENROLLMENTS,
  icpcPenaltyFromForm,
  shapeFromForm,
  type ContestCover,
  type Enrollment,
} from "@/lib/api/contests";
import { isId } from "@/lib/api/ids";
import { freezeFromForm } from "@/lib/api/leaderboard";
import { parseTables, SQL_MODES, type SqlMode } from "@/lib/api/policy";
import { serverRequest } from "@/lib/api/server";
import { instantFromWallClock } from "@/lib/format/datetime";
import { LOCALES, type Locale } from "@/lib/i18n/config";

export type SettingsState = {
  code?: string;
  saved?: boolean;
  rejected?: string[];
  /**
   * What the contest wears now, when the save was about its cover: the
   * picture the API has just stored, or `null` once it has been taken away.
   * Absent from every other save, which says nothing about the cover.
   *
   * It is carried because the upload answers with it, and because the panel
   * would otherwise have to ask for the picture again to show what it has
   * just sent.
   */
  cover?: ContestCover | null;
};

function oneOf<T extends string>(value: FormDataEntryValue | null, allowed: readonly T[]): T | null {
  return typeof value === "string" && (allowed as readonly string[]).includes(value)
    ? (value as T)
    : null;
}

async function attempt(
  path: string,
  init: { method: string; body: unknown },
  contestId: string,
): Promise<SettingsState> {
  const failure = await serverRequest(path, init).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) return { code: failure instanceof ApiError ? failure.code : "unreachable" };

  refresh(contestId);
  return { saved: true };
}

/**
 * What a saved contest makes stale: its own workspace, and the register that
 * lists it.
 */
function refresh(contestId: string): void {
  revalidatePath("/contests");
  revalidatePath(`/contests/${contestId}`, "layout");
}

/**
 * A datetime-local value, as the API wants it.
 *
 * The browser hands back a wall clock with no zone at all. Resolving it with
 * `new Date()` would resolve it in whatever zone this process runs in — UTC
 * inside the container, a developer's zone on a laptop — so the zone is named
 * instead, and it is the university's: the same one every timestamp in the
 * interface is already formatted in.
 */
function moment(value: FormDataEntryValue | null): string | null {
  const raw = String(value ?? "").trim();
  return raw ? instantFromWallClock(raw) : null;
}

/**
 * The contest's own fields.
 *
 * `PATCH` in meaning as well as on the wire: a field the body leaves out is
 * left unchanged by the endpoint. The fields this form can always edit are
 * sent every time; a field locked once the contest starts is omitted when its
 * control submitted nothing, and its absence means "unchanged", never
 * "cleared".
 *
 * Two different freezes apply, and the form obeys both. Settings stay editable
 * while the contest runs — extending the window after a power cut is exactly
 * what a running contest needs — but the shape does not: the question format,
 * the question order, the scoring mode, the timing model and the session
 * length are what people are already answering under. Their fieldset is
 * disabled once the contest starts, and a disabled radio group is excluded
 * from `FormData` entirely — `shapeFromForm` reads that absence as "send no
 * key" (see its own doc), the same "leave it alone" contract every other
 * lockable field on this form already follows, rather than falling back to
 * a hard-coded default that `checkRunningChange` on the Go side would then
 * refuse the whole save over.
 */
export async function saveSettingsAction(
  _previous: SettingsState,
  form: FormData,
): Promise<SettingsState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  const shape = shapeFromForm(form);
  if (!shape.ok) return { code: "invalid_request" };

  // The freeze is locked once the contest starts; a locked fieldset submits
  // nothing, which freezeFromForm turns into "send no key" rather than
  // "clear it".
  const freeze = freezeFromForm(
    form.get("leaderboardFreezeMode"),
    form.get("leaderboardFreezeAmount"),
    form.get("leaderboardFreezeUnit"),
  );
  if (!freeze.ok) return { code: "invalid_request" };
  const leaderboard: { names: string; freeze_min?: number | null } = {
    names: oneOf(form.get("leaderboardNames"), ["login", "full_name"] as const) ?? "login",
  };
  if (freeze.value !== undefined) leaderboard.freeze_min = freeze.value;

  // Locked with the rest of the shape, the same as `scoring` itself: a
  // disabled field submits nothing, which `icpcPenaltyFromForm` reads as
  // "send no key" rather than "clear it" — there is no cleared state for a
  // penalty in this mode.
  const icpcPenalty = icpcPenaltyFromForm(form.get("icpcPenaltyMin"));
  if (!icpcPenalty.ok) return { code: "invalid_request" };

  const rate = Number(form.get("queryRateLimitPerMin"));
  const grace = Number(form.get("gracePeriodMin"));

  // Blank means "no restriction", which is an empty list rather than a list
  // containing an empty string — the API would refuse that as an invalid CIDR.
  const allowedCidrs = String(form.get("allowedCidrs") ?? "")
    .split(/[\s,;]+/)
    .map((cidr) => cidr.trim())
    .filter(Boolean);

  const body: Record<string, unknown> = {
    enrollment: oneOf<Enrollment>(form.get("enrollment"), ENROLLMENTS) ?? "invite_only",
    ...shape.value,
    starts_at: moment(form.get("startsAt")),
    ends_at: moment(form.get("endsAt")),
    allowed_cidrs: allowedCidrs,
    leaderboard,
    settings: {
      enrollment_deadline: moment(form.get("enrollmentDeadline")) ?? "",
      query_rate_limit_per_min: Number.isFinite(rate) && rate >= 0 ? Math.floor(rate) : 0,
      grace_period_min: Number.isFinite(grace) && grace >= 0 ? Math.floor(grace) : 0,
    },
  };
  // Locked with the rest of the shape: a disabled field submits nothing, and
  // `icpcPenaltyFromForm` reads that as "send no key" — the same reasoning
  // `leaderboard.freeze_min` above already follows.
  if (icpcPenalty.value !== undefined) body.icpc_penalty_min = icpcPenalty.value;

  return attempt(`/contests/${contestId}`, { method: "PATCH", body }, contestId);
}

/**
 * The language set, replaced whole.
 *
 * It has to be: the default is enforced by a partial unique index, so moving
 * it from English to Romanian would otherwise collide with the English row
 * that has not been rewritten yet. Replacing the set makes the move
 * expressible at all.
 */
export async function saveLanguagesAction(
  _previous: SettingsState,
  form: FormData,
): Promise<SettingsState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  const chosen = form
    .getAll("languages")
    .filter((code): code is Locale => (LOCALES as readonly string[]).includes(String(code)));

  if (chosen.length === 0) return { code: "invalid_request" };

  const asked = oneOf(form.get("defaultLanguage"), LOCALES);
  const fallback = asked && chosen.includes(asked) ? asked : chosen[0];

  return attempt(
    `/contests/${contestId}/languages`,
    {
      method: "PUT",
      body: { languages: chosen.map((code) => ({ code, is_default: code === fallback })) },
    },
    contestId,
  );
}

/**
 * The SQL access policy.
 *
 * Table names are checked here as well as on the server. They become GRANT
 * statements when a participant's database is built, where they cannot be
 * passed as parameters — the narrow form is what makes that construction safe
 * whatever an author types. Checking early turns a rejected save into a list
 * of the entries that were wrong, which the server's single error code cannot
 * give.
 */
export async function savePolicyAction(
  _previous: SettingsState,
  form: FormData,
): Promise<SettingsState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  const mode = oneOf<SqlMode>(form.get("mode"), SQL_MODES) ?? "read_only";
  const { tables, rejected } = parseTables(String(form.get("writableTables") ?? ""));

  if (rejected.length > 0) return { code: "invalid_request", rejected };

  const quota = Number(form.get("diskQuotaRatio"));

  return attempt(
    `/contests/${contestId}/sql-policy`,
    {
      method: "PUT",
      body: {
        mode,
        // Only a read-write policy has writable tables; sending them with a
        // read-only mode describes access that mode does not grant.
        writable_tables: mode === "read_write" ? tables : [],
        allow_create_view: form.get("allowCreateView") === "on",
        allow_own_tables: form.get("allowOwnTables") === "on",
        allow_temp_tables: form.get("allowTempTables") === "on",
        allow_catalog: form.get("allowCatalog") === "on",
        disk_quota_ratio: Number.isFinite(quota) && quota > 0 ? Math.floor(quota) : 1,
      },
    },
    contestId,
  );
}

/**
 * The picture the contest wears.
 *
 * A Server Action like every other save on this screen, and for the same two
 * reasons: the form works with JavaScript off, and the API's origin never
 * reaches the browser. The bytes are not inspected here — what a file says
 * about itself is the uploader's claim, and the API decides by reading it.
 * A check on this side would be a second opinion that can be skipped by not
 * using this form.
 *
 * The one thing refused before the request is a missing credit line, and that
 * is not a second opinion: the account's upload budget counts refusals (the
 * work behind an upload is a decode and two resamples), so spending a place
 * in it to be told something this side already knows is a waste of the one
 * budget an organiser can actually run out of. The code returned is the API's
 * own, so the panel shows one message whichever side said it.
 */
export async function uploadCoverAction(
  _previous: SettingsState,
  form: FormData,
): Promise<SettingsState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  const file = form.get("file");
  if (!(file instanceof File) || file.size === 0) return { code: "invalid_request" };

  const attribution = String(form.get("attribution") ?? "").trim();
  if (!attribution) return { code: "cover_attribution_required" };

  // A form of this side's own making, carrying the two parts the endpoint
  // names and nothing else: the submitted one also holds the contest's
  // identifier, which belongs in the path rather than in the body.
  const payload = new FormData();
  payload.set("file", file);
  payload.set("attribution", attribution);

  const answer = await serverRequest(`/contests/${contestId}/cover`, {
    method: "PUT",
    rawBody: payload,
  }).then(
    (value: unknown) => ({ value }),
    (error: unknown) => ({ error }),
  );

  if ("error" in answer) {
    return { code: answer.error instanceof ApiError ? answer.error.code : "unreachable" };
  }

  refresh(contestId);
  return { saved: true, cover: contestCoverSchema.parse(answer.value) };
}

/**
 * Taking the picture away, which leaves the contest its drawn cover rather
 * than a gap (design spec §2.3).
 */
export async function removeCoverAction(
  _previous: SettingsState,
  form: FormData,
): Promise<SettingsState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  const failure = await serverRequest(`/contests/${contestId}/cover`, { method: "DELETE" }).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) return { code: failure instanceof ApiError ? failure.code : "unreachable" };

  refresh(contestId);
  return { saved: true, cover: null };
}
