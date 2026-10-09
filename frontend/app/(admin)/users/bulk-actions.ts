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
 * Server Actions on a selection of accounts (see `[userId]/actions.ts`). The
 * server validates everything again; this only catches what a form gets wrong
 * first: no account picked, a missing reason.
 */

export type BulkState = { code?: string; result?: BulkResult };
export type BulkPasswordState = { code?: string; result?: BulkPasswordResetResult };

/**
 * Ids from the form, validated. A malformed one means tampering (the register
 * rendered them), so it is dropped rather than reported.
 */
function ids(form: FormData): string[] {
  return form.getAll("ids").map(String).filter(isId);
}

/** Runs one bulk call and shapes the outcome for `useActionState`. */
async function run<T>(work: () => Promise<T>): Promise<{ code?: string; result?: T }> {
  const outcome = await work().then(
    (result) => ({ ok: true as const, result }),
    (error: unknown) => ({ ok: false as const, error }),
  );

  if (!outcome.ok) {
    return { code: outcome.error instanceof ApiError ? outcome.error.code : "unreachable" };
  }

  // The list must show the new statuses.
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

  // Activating needs no reason (the server clears it), so only block and delete
  // check.
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

  // An empty set is legitimate, as for one account.
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
