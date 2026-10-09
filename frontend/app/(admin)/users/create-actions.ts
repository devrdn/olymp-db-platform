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
 * Creates one account or imports a roster. The server enforces every rule; this
 * catches only an empty login or full name, and an empty or oversized paste.
 */

export type CreateAccountState = { code?: string; result?: CreatedAccount };
export type ImportAccountsState = { code?: string; result?: ImportResult };

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

  const outcome = await createAccount({ login, fullName, email, roles: checkedRoles(form) }).then(
    (result) => ({ ok: true as const, result }),
    (error: unknown) => ({ ok: false as const, error }),
  );

  if (!outcome.ok) {
    return { code: outcome.error instanceof ApiError ? outcome.error.code : "unreachable" };
  }

  // The new account must appear in the register.
  revalidatePath("/users", "layout");
  return { result: outcome.result };
}

export async function importAccountsAction(
  _previous: ImportAccountsState,
  form: FormData,
): Promise<ImportAccountsState> {
  const rows = parseRoster(String(form.get("roster") ?? ""));
  // An empty paste or more than `MAX_IMPORT_ROWS` lines (mirrors
  // `users.maxImportRows`) is refused before the request.
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
