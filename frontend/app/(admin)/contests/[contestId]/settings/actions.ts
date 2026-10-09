"use server";

import { revalidatePath } from "next/cache";

import { ApiError, failureCode } from "@/lib/api/client";
import { type ContestCover, contestCoverSchema, type Enrollment, ENROLLMENTS, enumFromForm, icpcPenaltyFromForm, shapeFromForm } from "@/lib/api/contests";
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
   * After a cover save: the stored picture, or `null` once removed, so the
   * panel need not fetch it again. Absent from other saves.
   */
  cover?: ContestCover | null;
};

async function attempt(
  path: string,
  init: { method: string; body: unknown },
  contestId: string,
): Promise<SettingsState> {
  const failure = await serverRequest(path, init).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) return { code: failureCode(failure) };

  refresh(contestId);
  return { saved: true };
}

/** Revalidates the contest's workspace and the register. */
function refresh(contestId: string): void {
  revalidatePath("/contests");
  revalidatePath(`/contests/${contestId}`, "layout");
}

/**
 * A datetime-local value for the API. The browser gives a wall clock with no
 * zone; `new Date()` would use the process zone (UTC in the container), so the
 * university's zone is named explicitly.
 */
function moment(value: FormDataEntryValue | null): string | null {
  const raw = String(value ?? "").trim();
  return raw ? instantFromWallClock(raw) : null;
}

/**
 * Saves the contest's fields. A field left out is unchanged. Settings stay
 * editable while the contest runs (extending the window after a power cut), but
 * the shape (format, order, scoring, timing, session length) freezes; its
 * disabled fieldset submits nothing, which is read as "send no key" rather than
 * a default the server would refuse.
 */
export async function saveSettingsAction(
  _previous: SettingsState,
  form: FormData,
): Promise<SettingsState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  const shape = shapeFromForm(form);
  if (!shape.ok) return { code: "invalid_request" };

  // Locked once the contest starts; nothing submitted means "send no key".
  const freeze = freezeFromForm(
    form.get("leaderboardFreezeMode"),
    form.get("leaderboardFreezeAmount"),
    form.get("leaderboardFreezeUnit"),
  );
  if (!freeze.ok) return { code: "invalid_request" };
  const leaderboard: { names: string; freeze_min?: number | null } = {
    names: enumFromForm(form.get("leaderboardNames"), ["login", "full_name"] as const) ?? "login",
  };
  if (freeze.value !== undefined) leaderboard.freeze_min = freeze.value;

  // Locked with the shape; nothing submitted means "send no key".
  const icpcPenalty = icpcPenaltyFromForm(form.get("icpcPenaltyMin"));
  if (!icpcPenalty.ok) return { code: "invalid_request" };

  const rate = Number(form.get("queryRateLimitPerMin"));
  const grace = Number(form.get("gracePeriodMin"));

  // Blank means no restriction: an empty list, not `[""]`, which the API would
  // refuse as an invalid CIDR.
  const allowedCidrs = String(form.get("allowedCidrs") ?? "")
    .split(/[\s,;]+/)
    .map((cidr) => cidr.trim())
    .filter(Boolean);

  const body: Record<string, unknown> = {
    enrollment: enumFromForm<Enrollment>(form.get("enrollment"), ENROLLMENTS) ?? "invite_only",
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
  if (icpcPenalty.value !== undefined) body.icpc_penalty_min = icpcPenalty.value;

  return attempt(`/contests/${contestId}`, { method: "PATCH", body }, contestId);
}

/**
 * Replaces the language set whole: the default is a partial unique index, so
 * moving it row by row would collide.
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

  const asked = enumFromForm(form.get("defaultLanguage"), LOCALES);
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
 * Saves the SQL policy. Table names are also checked here, to list the bad
 * entries; the server checks them too, since they become GRANT statements
 * that cannot take parameters.
 */
export async function savePolicyAction(
  _previous: SettingsState,
  form: FormData,
): Promise<SettingsState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  const mode = enumFromForm<SqlMode>(form.get("mode"), SQL_MODES) ?? "read_only";
  const { tables, rejected } = parseTables(String(form.get("writableTables") ?? ""));

  if (rejected.length > 0) return { code: "invalid_request", rejected };

  const quota = Number(form.get("diskQuotaRatio"));

  return attempt(
    `/contests/${contestId}/sql-policy`,
    {
      method: "PUT",
      body: {
        mode,
        // Only read-write grants writable tables.
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
 * Uploads the cover. The bytes are not inspected here; the API decides by
 * reading them. A missing credit line is refused before the request, because
 * refusals count against the account's upload budget.
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

  // Only the two parts the endpoint names; the contest id belongs in the path.
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

/** Removes the picture; the contest falls back to its drawn cover. */
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

  if (failure) return { code: failureCode(failure) };

  refresh(contestId);
  return { saved: true, cover: null };
}
