/**
 * The words an account's state is spelled with.
 *
 * Apart from the schemas next door, and the separation is a size rather than a
 * taste: a schema module calls `z.object()` at load, so a bundler cannot drop
 * it, and a client component importing one string array from it was shipping
 * the whole of zod — 280 KB of parser to render two filter buttons. The
 * schemas import these, so there is still one definition of what a status is.
 */
export const ACCOUNT_STATUSES = ["active", "blocked", "deleted"] as const;
export type AccountStatus = (typeof ACCOUNT_STATUSES)[number];

/**
 * Why an account in a selection did not change.
 *
 * The server's own vocabulary (users.SkipNotFound and its siblings in
 * backend/internal/users/bulk.go), mirrored here so the selection bar can
 * render every outcome. A reason this list does not name is shown raw rather
 * than dropped or rejected: an unnamed outcome is still an outcome the
 * administrator has to see, which is why the schema next door parses `reason`
 * as a bare string instead of restricting it to this list.
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

/** The most accounts one operation carries. Mirrors users.MaxBulkAccounts. */
export const MAX_BULK_ACCOUNTS = 500;

/**
 * Why one row of a roster import produced no account.
 *
 * A separate vocabulary from SKIP_REASONS above, and a smaller one: a bulk
 * status/roles/password-reset operation acts on accounts that already exist,
 * so it can find one gone, itself, the last administrator. Import only ever
 * creates one, so a row can fail it in exactly three ways — the server's own
 * (`users.SkipLoginTaken` and its siblings in
 * backend/internal/users/service.go's `Import`). A reason this list does not
 * name is shown raw rather than dropped, the same rule SKIP_REASONS follows.
 */
export const IMPORT_SKIP_REASONS = ["login_taken", "email_taken", "invalid_row"] as const;
export type ImportSkipReason = (typeof IMPORT_SKIP_REASONS)[number];

/** The most rows one import carries. Mirrors users.maxImportRows. */
export const MAX_IMPORT_ROWS = 500;
