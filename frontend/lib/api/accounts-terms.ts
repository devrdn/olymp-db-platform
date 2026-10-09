/** The vocabulary of an account, apart from the wire schemas (see content-terms.ts for why). */
export const ACCOUNT_STATUSES = ["active", "blocked", "deleted"] as const;
export type AccountStatus = (typeof ACCOUNT_STATUSES)[number];

/**
 * Why an account in a bulk selection did not change (`users.Skip*`). A reason
 * not listed here is shown raw.
 */
export const SKIP_REASONS = [
  "not_found",
  "self",
  "last_administrator",
  "already_in_status",
  "deleted",
  "login_taken",
  "email_taken",
] as const;
export type SkipReason = (typeof SKIP_REASONS)[number];

/** Mirrors `users.MaxBulkAccounts`. */
export const MAX_BULK_ACCOUNTS = 500;

/**
 * Why a roster row produced no account. Smaller than SKIP_REASONS because an
 * import only creates accounts. A reason not listed here is shown raw.
 */
export const IMPORT_SKIP_REASONS = ["login_taken", "email_taken", "invalid_row"] as const;
export type ImportSkipReason = (typeof IMPORT_SKIP_REASONS)[number];

/** Mirrors `users.maxImportRows`. */
export const MAX_IMPORT_ROWS = 500;
