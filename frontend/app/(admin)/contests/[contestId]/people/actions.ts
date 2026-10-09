"use server";

import { revalidatePath } from "next/cache";

import { ApiError, failureCode } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { importResultSchema, parseLogins, type ImportResult } from "@/lib/api/people";
import { serverRequest } from "@/lib/api/server";

export type PeopleState = { code?: string; done?: boolean; skipReason?: string };
export type ImportState = { code?: string; result?: ImportResult };

/** Both ids reach a request path, so both are validated first. */
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

  if (failure) return { code: failureCode(failure) };

  revalidatePath(`/contests/${contestId}`, "layout");
  return { done: true };
}

/** Appoints a manager. The role is fixed: ownership is never granted through this list. */
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
 * Removes someone who has not started. Someone who has can only be
 * disqualified, which keeps their record; the API enforces this.
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
 * Adds one participant from the picker. Sends `user_ids`, since the picker
 * already resolved the account. Parsed with `importResultSchema`, not
 * `attempt()`: the endpoint answers 200 with `{ added, skipped }` even for
 * someone already enrolled, and a skip must not read as success.
 */
export async function addParticipantAction(
  _previous: PeopleState,
  form: FormData,
): Promise<PeopleState> {
  const at = pair(form);
  if (!at) return { code: "invalid_user_id" };

  const outcome = await serverRequest(`/contests/${at.contestId}/participants`, {
    method: "POST",
    body: { user_ids: [at.userId] },
  }).then(
    (payload) => importResultSchema.parse(payload),
    (error: unknown) => (error instanceof ApiError ? error : null),
  );

  if (outcome instanceof ApiError) return { code: outcome.code };
  if (outcome === null) return { code: "unreachable" };
  if (outcome.added === 0) return { skipReason: outcome.skipped[0]?.reason };

  revalidatePath(`/contests/${at.contestId}`, "layout");
  return { done: true };
}

/**
 * Imports participants by login. One typo must not reject the rest, so the
 * result is a partial success naming every rejected line; it is returned
 * because a revalidated list cannot show what failed.
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
