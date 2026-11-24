"use server";

import { isId } from "@/lib/api/ids";
import { importResultSchema, parseLogins, type ImportResult } from "@/lib/api/people";
import { serverAction, serverRequest, type ActionState } from "@/lib/api/server";

import { contestChanged } from "../revalidation";

export type PeopleState = ActionState & { skipReason?: string };
export type ImportState = ActionState & { result?: ImportResult };

/** Both ids reach a request path, so both are validated first. */
function pair(form: FormData): { contestId: string; userId: string } | null {
  const contestId = form.get("contestId");
  const userId = form.get("userId");
  return isId(contestId) && isId(userId) ? { contestId, userId } : null;
}

function change(contestId: string, path: string, init: { method: string; body?: unknown }): Promise<PeopleState> {
  return serverAction(() => serverRequest(path, init), { revalidate: contestChanged(contestId) });
}

/** Appoints a manager. The role is fixed: ownership is never granted through this list. */
export async function grantManagerAction(
  _previous: PeopleState,
  form: FormData,
): Promise<PeopleState> {
  const at = pair(form);
  if (!at) return { code: "invalid_user_id" };

  return change(at.contestId, `/contests/${at.contestId}/managers/${at.userId}`, {
    method: "PUT",
    body: { role: "manager" },
  });
}

export async function revokeManagerAction(
  _previous: PeopleState,
  form: FormData,
): Promise<PeopleState> {
  const at = pair(form);
  if (!at) return { code: "invalid_user_id" };

  return change(at.contestId, `/contests/${at.contestId}/managers/${at.userId}`, { method: "DELETE" });
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

  return change(at.contestId, `/contests/${at.contestId}/participants/${at.userId}`, { method: "DELETE" });
}

export async function disqualifyParticipantAction(
  _previous: PeopleState,
  form: FormData,
): Promise<PeopleState> {
  const at = pair(form);
  if (!at) return { code: "invalid_user_id" };

  return change(at.contestId, `/contests/${at.contestId}/participants/${at.userId}/disqualify`, { method: "POST" });
}

/**
 * Adds one participant from the picker. Sends `user_ids`, since the picker
 * already resolved the account. The endpoint answers 200 with
 * `{ added, skipped }` even for someone already enrolled, and a skip must not
 * read as success, so nothing is revalidated until the count says so.
 */
export async function addParticipantAction(
  _previous: PeopleState,
  form: FormData,
): Promise<PeopleState> {
  const at = pair(form);
  if (!at) return { code: "invalid_user_id" };

  return serverAction(
    async () =>
      importResultSchema.parse(
        await serverRequest(`/contests/${at.contestId}/participants`, {
          method: "POST",
          body: { user_ids: [at.userId] },
        }),
      ),
    {
      revalidate: (result) => (result.added === 0 ? [] : contestChanged(at.contestId)),
      success: (result) => (result.added === 0 ? { skipReason: result.skipped[0]?.reason } : { ok: true }),
    },
  );
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

  return serverAction(
    async () =>
      importResultSchema.parse(
        await serverRequest(`/contests/${contestId}/participants`, { method: "POST", body: { logins } }),
      ),
    { revalidate: contestChanged(contestId), success: (result) => ({ ok: true, result }) },
  );
}
