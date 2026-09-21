import type { FeedItem, LoggedQuery } from "@/lib/api/monitor";

/** Builders shared by this page's tests. */

export const CONTEST = "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";
export const REG = "9a1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";

/** The query builder of the shared log, so both sides build the same row. */
export { loggedQuery } from "@/components/product/test-fixtures";

/** The same query as the timeline carries it. */
export function queryFeedItem(query: LoggedQuery): FeedItem {
  return {
    cursor: `t${query.id}`,
    at: query.executedAt,
    kind: "query",
    registrationId: REG,
    login: "ivanov",
    fullName: "Ivan Ivanov",
    detail: {
      type: "query",
      id: query.id,
      sql: query.sql,
      sqlTruncated: query.sqlTruncated,
      status: query.status,
      error: query.error,
      durationMs: query.durationMs,
      rowCount: query.rowCount,
      ip: query.ip,
    },
  };
}

export function setVisibility(state: "visible" | "hidden") {
  Object.defineProperty(document, "visibilityState", { value: state, configurable: true });
  Object.defineProperty(document, "hidden", { value: state === "hidden", configurable: true });
  document.dispatchEvent(new Event("visibilitychange"));
}
