"use server";

import { revalidatePath } from "next/cache";

import {
  bulkReplaceRoles,
  bulkResetPassword,
  bulkSetStatus,
  type AccountStatus,
  type BulkPasswordResetResult,
  type BulkResult,
} from "@/lib/api/accounts";
import { ApiError } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";

/**
 * What an administrator can do to a selection of accounts.
 *
 * Server Actions rather than fetches from the browser, for the same reasons
 * `[userId]/actions.ts` gives: the forms work with JavaScript switched off,
 * the API's origin never reaches the page, and Next checks the request's
 * Origin against its Host before any of this runs.
 *
 * Every one of these is refused again by the server — `bulkSetStatus` and its
 * siblings already validate the reason, the selection size and every other
 * rule. What is checked here is only what a form can get wrong before it ever
 * reaches them: no account picked, an empty reason where one is required.
 */

export type BulkState = { code?: string; result?: BulkResult };
export type BulkPasswordState = { code?: string; result?: BulkPasswordResetResult };

/**
 * Every id the form carried, each checked before it reaches a request body.
 *
 * A row that fails `isId` is dropped rather than turning the whole submission
 * into a 400: the selection is built client-side from ids the register itself
 * rendered, so a malformed one here means tampering, not a typo worth
 * reporting back.
 */
function ids(form: FormData): string[] {
  return form.getAll("ids").map(String).filter(isId);
}

/** Runs one bulk call and turns its outcome into what `useActionState` wants. */
async function run<T>(work: () => Promise<T>): Promise<{ code?: string; result?: T }> {
  const outcome = await work().then(
    (result) => ({ ok: true as const, result }),
    (error: unknown) => ({ ok: false as const, error }),
  );

  if (!outcome.ok) {
    return { code: outcome.error instanceof ApiError ? outcome.error.code : "unreachable" };
  }

  // A changed or deleted account has to stop reading its old status the next
  // time this list is rendered — the same reason the single-account actions
  // revalidate.
  revalidatePath("/users", "layout");
  return { result: outcome.result };
}

async function setStatus(
  form: FormData,
  status: AccountStatus,
  requireReason: boolean,
): Promise<BulkState> {
  const picked = ids(form);
  if (picked.length === 0) return { code: "invalid_request" };

  // Going back to active needs no justification — bulkSetStatus clears the
  // reason itself for that destination — so only block and delete check here.
  const reason = String(form.get("reason") ?? "").trim();
  if (requireReason && reason === "") return { code: "reason_required" };

  return run(() => bulkSetStatus(picked, status, reason));
}

export async function bulkBlockAction(_previous: BulkState, form: FormData): Promise<BulkState> {
  return setStatus(form, "blocked", true);
}

export async function bulkUnblockAction(
  _previous: BulkState,
  form: FormData,
): Promise<BulkState> {
  return setStatus(form, "active", false);
}

export async function bulkDeleteAction(_previous: BulkState, form: FormData): Promise<BulkState> {
  return setStatus(form, "deleted", true);
}

export async function bulkReplaceRolesAction(
  _previous: BulkState,
  form: FormData,
): Promise<BulkState> {
  const picked = ids(form);
  if (picked.length === 0) return { code: "invalid_request" };

  // Every checked box, and an unchecked set is a legitimate answer — the same
  // reasoning as the single-account `replaceRolesAction`.
  const roles = form.getAll("roles").map(String).filter(Boolean);

  return run(() => bulkReplaceRoles(picked, roles));
}

export async function bulkResetPasswordAction(
  _previous: BulkPasswordState,
  form: FormData,
): Promise<BulkPasswordState> {
  const picked = ids(form);
  if (picked.length === 0) return { code: "invalid_request" };

  return run(() => bulkResetPassword(picked));
}
