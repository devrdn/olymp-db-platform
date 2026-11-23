"use server";

import {
  createAccount,
  importAccounts,
  type CreatedAccount,
  type ImportResult,
} from "@/lib/api/accounts";
import { MAX_IMPORT_ROWS } from "@/lib/api/accounts-terms";
import { serverAction, type ActionState, type Revalidation } from "@/lib/api/server";

import { sendByEmail } from "./delivery";
import { parseRoster } from "./roster";

/**
 * Creates one account or imports a roster. The server enforces every rule; this
 * catches only an empty login or full name, and an empty or oversized paste.
 */

export type CreateAccountState = ActionState & { result?: CreatedAccount };
export type ImportAccountsState = ActionState & { result?: ImportResult };

/** A new account must appear in the register. */
const CHANGED: Revalidation[] = [{ layout: "/users" }];

/** Ticked roles; an empty set is legitimate. */
function checkedRoles(form: FormData): string[] {
  return form.getAll("roles").map(String).filter(Boolean);
}

export async function createAccountAction(
  _previous: CreateAccountState,
  form: FormData,
): Promise<CreateAccountState> {
  const login = String(form.get("login") ?? "").trim();
  const fullName = String(form.get("full_name") ?? "").trim();
  // Caught before the request; the server would refuse it as ErrInvalidAccount.
  if (login === "" || fullName === "") return { code: "invalid_request" };

  const email = String(form.get("email") ?? "").trim();

  return serverAction(
    () => createAccount({ login, fullName, email, roles: checkedRoles(form), sendByEmail: sendByEmail(form) }),
    { revalidate: CHANGED, success: (result) => ({ ok: true, result }) },
  );
}

export async function importAccountsAction(
  _previous: ImportAccountsState,
  form: FormData,
): Promise<ImportAccountsState> {
  const rows = parseRoster(String(form.get("roster") ?? ""));
  // An empty paste or more than `MAX_IMPORT_ROWS` lines (mirrors
  // `users.maxImportRows`) is refused before the request.
  if (rows.length === 0 || rows.length > MAX_IMPORT_ROWS) return { code: "invalid_request" };

  return serverAction(() => importAccounts(rows, checkedRoles(form), sendByEmail(form)), {
    revalidate: CHANGED,
    success: (result) => ({ ok: true, result }),
  });
}
