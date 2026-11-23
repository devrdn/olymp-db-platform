/**
 * The vocabulary of a participant's record, apart from the wire schemas (see
 * content-terms.ts for why).
 */

/** Also the only values a queries filter takes (`monitor.queryStatuses`). */
export const QUERY_STATUSES = ["running", "ok", "error", "rejected", "timeout"] as const;

/** The most rows one page of queries carries (`monitor.MaxQueriesPage`). */
export const MAX_QUERIES_PAGE = 50;

/** The most characters a query search may hold (`monitor.MaxQuerySearchRunes`). */
export const MAX_QUERY_SEARCH = 200;
