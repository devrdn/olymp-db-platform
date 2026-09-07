"use server";

import { revalidatePath } from "next/cache";

import {
  createAccount,
  importAccounts,
  type CreatedAccount,
  type ImportResult,
} from "@/lib/api/accounts";
import { ApiError } from "@/lib/api/client";
import { MAX_IMPORT_ROWS } from "@/lib/api/accounts-terms";

import { parseRoster } from "./roster";

/**
 * Registering an account: one at a time, or a whole roster at once.
 *
 * Server Actions rather than fetches from the browser, for the same reasons
 * every other write on this screen gives (`[userId]/actions.ts`,
 * `bulk-actions.ts`): the forms work with JavaScript switched off, the API's
 * origin never reaches the page, and Next checks the request's Origin
 * against its Host before any of this runs.
 *
 * Both endpoints are refused again by the server — `createAccount` and
 * `importAccounts` already carry every rule (login and full name required,
 * the bounds on each field, the roster's own size limit). What is checked
 * here is only what a form can get wrong before it ever reaches them: no
 * login, no full name, nothing pasted.
 */

export type CreateAccountState = { code?: string; result?: CreatedAccount };
export type ImportAccountsState = { code?: string; result?: ImportResult };

/** Every role box ticked on either dialog. An empty set is legitimate — an
 * account with no role can sign in and do nothing, exactly as the
 * single-account and bulk role forms already allow. */
function checkedRoles(form: FormData): string[] {
  return form.getAll("roles").map(String).filter(Boolean);
}

export async function createAccountAction(
  _previous: CreateAccountState,
  form: FormData,
): Promise<CreateAccountState> {
  const login = String(form.get("login") ?? "").trim();
  const fullName = String(form.get("full_name") ?? "").trim();
  // Caught here, before the request ever leaves: the server refuses either
  // as ErrInvalidAccount, but this is what stops an obviously empty form
  // from spending a round trip to be told so.
  if (login === "" || fullName === "") return { code: "invalid_request" };

  const email = String(form.get("email") ?? "").trim();

  const outcome = await createAccount({ login, fullName, email, roles: checkedRoles(form) }).then(
    (result) => ({ ok: true as const, result }),
    (error: unknown) => ({ ok: false as const, error }),
  );

  if (!outcome.ok) {
    return { code: outcome.error instanceof ApiError ? outcome.error.code : "unreachable" };
  }

  // A freshly created account has to appear the next time this register is
  // rendered — the same reason every other write on this screen revalidates.
  revalidatePath("/users", "layout");
  return { result: outcome.result };
}

export async function importAccountsAction(
  _previous: ImportAccountsState,
  form: FormData,
): Promise<ImportAccountsState> {
  const rows = parseRoster(String(form.get("roster") ?? ""));
  // Caught here for the same reason the empty-selection guards in
  // `bulk-actions.ts` are: an empty paste, or one line too large for one
  // import (`users.maxImportRows`, mirrored in `MAX_IMPORT_ROWS`), is exactly
  // what the server would refuse as `invalid_request` — checked before the
  // request leaves rather than after.
  if (rows.length === 0 || rows.length > MAX_IMPORT_ROWS) return { code: "invalid_request" };

  const outcome = await importAccounts(rows, checkedRoles(form)).then(
    (result) => ({ ok: true as const, result }),
    (error: unknown) => ({ ok: false as const, error }),
  );

  if (!outcome.ok) {
    return { code: outcome.error instanceof ApiError ? outcome.error.code : "unreachable" };
  }

  revalidatePath("/users", "layout");
  return { result: outcome.result };
}
