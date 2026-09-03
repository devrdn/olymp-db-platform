/**
 * The words an account's state is spelled with.
 *
 * Apart from the schemas next door, and the separation is a size rather than a
 * taste: a schema module calls `z.object()` at load, so a bundler cannot drop
 * it, and a client component importing one string array from it was shipping
 * the whole of zod — 280 KB of parser to render two filter buttons. The
 * schemas import these, so there is still one definition of what a status is.
 */
export const ACCOUNT_STATUSES = ["active", "blocked"] as const;
export type AccountStatus = (typeof ACCOUNT_STATUSES)[number];
