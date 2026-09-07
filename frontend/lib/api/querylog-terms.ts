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
