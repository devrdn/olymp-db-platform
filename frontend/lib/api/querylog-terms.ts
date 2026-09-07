/**
 * The vocabulary of a participant's own query log, apart from the schema that
 * validates the wire.
 *
 * A schema module calls `z.object()` when it loads, so a bundler cannot drop
 * it — see content-terms.ts's own doc for why a client component reaches for
 * this file instead and never for querylog.ts directly.
 */

/**
 * One page of the query log, and the ceiling `HistoryPanel`'s own "load more"
 * will ask for at once.
 *
 * Mirrors the backend's own bound exactly
 * (queryrunner.DefaultHistoryLimit) rather than inventing a second number: a
 * contest can run two hours, and a participant who never stops querying can
 * put hundreds of rows in their own log — unbounded, that is hundreds of
 * table rows re-rendered on every "load more" and, eventually, a query
 * string the server clamps anyway. Fifty is small enough that a page of rows
 * costs nothing to render and large enough that "load more" is rare rather
 * than constant.
 */
export const QUERY_LOG_PAGE_SIZE = 50;

/**
 * The minimum gap, in milliseconds, between two automatic refreshes of the
 * query log triggered by switching onto its tab (`QueryLogPanel`'s own
 * doc, finding 3 — and finding 4 of the follow-up review that tightened
 * this). Each of those refreshes calls `AdmitRead`, which shares its
 * per-minute budget with `Run`: a student idly toggling Result and Log back
 * and forth spends that budget on identical data rather than on their next
 * query, unless something makes a transition inside this window a no-op.
 * Three seconds is long enough that no legitimate "I want to see whether
 * that last query landed" re-entry is ever this fast, and short enough that
 * a genuine return to the tab after doing other work still refreshes right
 * away.
 */
export const QUERY_LOG_REFRESH_MIN_INTERVAL_MS = 3000;
