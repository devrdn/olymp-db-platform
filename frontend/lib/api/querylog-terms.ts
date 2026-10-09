/**
 * The vocabulary of a participant's own query log, apart from the wire schema
 * (see content-terms.ts for why).
 */

/** One "load more" page; mirrors `queryrunner.DefaultHistoryLimit`. */
export const QUERY_LOG_PAGE_SIZE = 50;

/**
 * The minimum gap between refreshes triggered by switching onto the log tab.
 * Each refresh spends the per-minute budget shared with running queries, so
 * toggling tabs must not drain it.
 */
export const QUERY_LOG_REFRESH_MIN_INTERVAL_MS = 3000;
