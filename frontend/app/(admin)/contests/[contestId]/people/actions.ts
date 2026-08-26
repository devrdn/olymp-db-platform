"use server";

import { revalidatePath } from "next/cache";

import { ApiError } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { importResultSchema, parseLogins, type ImportResult } from "@/lib/api/people";
import { serverRequest } from "@/lib/api/server";

export type PeopleState = { code?: string; done?: boolean };
export type ImportState = { code?: string; result?: ImportResult };

/** Both identifiers reach a request path, so both are checked before they do. */
function pair(form: FormData): { contestId: string; userId: string } | null {
  const contestId = form.get("contestId");
  const userId = form.get("userId");
  return isId(contestId) && isId(userId) ? { contestId, userId } : null;
}

async function attempt(path: string, init: { method: string; body?: unknown }, contestId: string) {
  const failure = await serverRequest(path, init).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) return { code: failure instanceof ApiError ? failure.code : "unreachable" };

  revalidatePath(`/contests/${contestId}`, "layout");
  return { done: true };
}

/**
 * Appointing a manager.
 *
 * The role is fixed rather than chosen. Ownership is not granted through this
 * list: two owners make "who may appoint" ambiguous and none leaves the
 * contest with nobody who can appoint anyone. Handing over a contest is a
 * separate act, not a quiet side effect of editing its staff.
 */
export async function grantManagerAction(
  _previous: PeopleState,
  form: FormData,
): Promise<PeopleState> {
  const at = pair(form);
  if (!at) return { code: "invalid_user_id" };

  return attempt(
    `/contests/${at.contestId}/managers/${at.userId}`,
    { method: "PUT", body: { role: "manager" } },
    at.contestId,
  );
}

export async function revokeManagerAction(
  _previous: PeopleState,
  form: FormData,
): Promise<PeopleState> {
  const at = pair(form);
  if (!at) return { code: "invalid_user_id" };

  return attempt(
    `/contests/${at.contestId}/managers/${at.userId}`,
    { method: "DELETE" },
    at.contestId,
  );
}

/**
 * Removing someone who has not started.
 *
 * Somebody who has started cannot be deleted: their queries and answers are
 * part of the record of the contest. Excluding them is a disqualification,
 * which keeps everything they did. The API enforces the distinction; the
 * interface offers whichever control actually applies.
 */
export async function removeParticipantAction(
  _previous: PeopleState,
  form: FormData,
): Promise<PeopleState> {
  const at = pair(form);
  if (!at) return { code: "invalid_user_id" };

  return attempt(
    `/contests/${at.contestId}/participants/${at.userId}`,
    { method: "DELETE" },
    at.contestId,
  );
}

export async function disqualifyParticipantAction(
  _previous: PeopleState,
  form: FormData,
): Promise<PeopleState> {
  const at = pair(form);
  if (!at) return { code: "invalid_user_id" };

  return attempt(
    `/contests/${at.contestId}/participants/${at.userId}/disqualify`,
    { method: "POST" },
    at.contestId,
  );
}

/**
 * Importing a list of participants.
 *
 * Logins, not identifiers: what an organiser has in hand is a column of
 * student numbers copied out of a spreadsheet. One typo must not reject the
 * other two hundred and ninety-nine rows, so the answer is an honest partial
 * success — every line that did not go in, named with its reason, so it can be
 * found again in the spreadsheet it came from.
 *
 * The result is returned rather than only revalidated. Revalidating shows the
 * list that did import; it cannot show which rows did not.
 */
export async function importParticipantsAction(
  _previous: ImportState,
  form: FormData,
): Promise<ImportState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  const logins = parseLogins(String(form.get("logins") ?? ""));
  if (logins.length === 0) return { code: "invalid_request" };

  const outcome = await serverRequest(`/contests/${contestId}/participants`, {
    method: "POST",
    body: { logins },
  }).then(
    (payload) => importResultSchema.parse(payload),
    (error: unknown) => (error instanceof ApiError ? error : null),
  );

  if (outcome instanceof ApiError) return { code: outcome.code };
  if (outcome === null) return { code: "unreachable" };

  revalidatePath(`/contests/${contestId}`, "layout");

  return { result: outcome };
}
