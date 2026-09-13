"use server";

import { revalidatePath } from "next/cache";

import { ApiError } from "@/lib/api/client";
import {
  ENROLLMENTS,
  PROGRESSIONS,
  QUESTION_MODES,
  SCORINGS,
  TIMINGS,
  type Enrollment,
  type Progression,
  type QuestionMode,
  type Scoring,
  type Timing,
} from "@/lib/api/contests";
import { isId } from "@/lib/api/ids";
import { freezeFromForm } from "@/lib/api/leaderboard";
import { parseTables, SQL_MODES, type SqlMode } from "@/lib/api/policy";
import { serverRequest } from "@/lib/api/server";
import { instantFromWallClock } from "@/lib/format/datetime";
import { LOCALES, type Locale } from "@/lib/i18n/config";

export type SettingsState = { code?: string; saved?: boolean; rejected?: string[] };

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

  revalidatePath("/contests");
  revalidatePath(`/contests/${contestId}`, "layout");
  return { saved: true };
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
 * `PATCH` on the wire, a full replacement in meaning: the endpoint takes every
 * field and writes every field, so the form has to submit all of them. Sending
 * a subset would clear whatever it left out.
 *
 * Two different freezes apply, and the form obeys both. Settings stay editable
 * while the contest runs — extending the window after a power cut is exactly
 * what a running contest needs — but the shape does not: the question format,
 * the question order, the scoring mode, the timing model and the session
 * length are what people are already answering under. The frozen fields are
 * submitted unchanged from what the contest already holds, so a running
 * contest's shape survives a save of its schedule.
 */
export async function saveSettingsAction(
  _previous: SettingsState,
  form: FormData,
): Promise<SettingsState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  const timing = oneOf<Timing>(form.get("timing"), TIMINGS) ?? "fixed";
  const duration = Number(form.get("durationMin"));
  const durationMin =
    timing === "individual" && Number.isFinite(duration) && duration > 0
      ? Math.floor(duration)
      : null;

  if (timing === "individual" && durationMin === null) return { code: "invalid_request" };

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

  const rate = Number(form.get("queryRateLimitPerMin"));
  const grace = Number(form.get("gracePeriodMin"));

  // Blank means "no restriction", which is an empty list rather than a list
  // containing an empty string — the API would refuse that as an invalid CIDR.
  const allowedCidrs = String(form.get("allowedCidrs") ?? "")
    .split(/[\s,;]+/)
    .map((cidr) => cidr.trim())
    .filter(Boolean);

  return attempt(
    `/contests/${contestId}`,
    {
      method: "PATCH",
      body: {
        enrollment: oneOf<Enrollment>(form.get("enrollment"), ENROLLMENTS) ?? "invite_only",
        question_mode: oneOf<QuestionMode>(form.get("questionMode"), QUESTION_MODES) ?? "multi",
        progression: oneOf<Progression>(form.get("progression"), PROGRESSIONS) ?? "free",
        scoring: oneOf<Scoring>(form.get("scoring"), SCORINGS) ?? "points",
        timing,
        duration_min: durationMin,
        starts_at: moment(form.get("startsAt")),
        ends_at: moment(form.get("endsAt")),
        allowed_cidrs: allowedCidrs,
        leaderboard,
        settings: {
          enrollment_deadline: moment(form.get("enrollmentDeadline")) ?? "",
          query_rate_limit_per_min: Number.isFinite(rate) && rate >= 0 ? Math.floor(rate) : 0,
          grace_period_min: Number.isFinite(grace) && grace >= 0 ? Math.floor(grace) : 0,
        },
      },
    },
    contestId,
  );
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
