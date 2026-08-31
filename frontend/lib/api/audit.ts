import { z } from "zod";

/**
 * The trail of who did what, from where and when.
 *
 * Read-only by construction: entries are written in the same transaction as
 * the action they describe and never arrive over HTTP, so there is nothing
 * here that posts.
 */

export const auditEntrySchema = z.object({
  id: z.number(),
  actor_id: z.string().optional(),
  /**
   * Empty for a system event, and for an account deleted since. The trail
   * outlives the people in it, which is the point of keeping one — so the
   * absence of a name is information, not a gap to hide.
   */
  actor_login: z.string().optional(),
  action: z.string(),
  entity: z.string().optional(),
  entity_id: z.string().optional(),
  payload: z.record(z.string(), z.unknown()).optional(),
  ip: z.string().optional(),
  user_agent: z.string().optional(),
  created_at: z.string(),
});

export const auditPageSchema = z.object({
  items: z.array(auditEntrySchema),
  total: z.number(),
});

export type AuditEntry = z.infer<typeof auditEntrySchema>;

/** What the filters may narrow by. Each one narrows an index the table has. */
export type AuditQuery = {
  actor?: string;
  action?: string;
  entity?: string;
  entityId?: string;
  from?: string;
  to?: string;
  offset?: number;
};

/** How many entries one page shows. */
export const AUDIT_PAGE = 50;

/**
 * The query string for a page of the trail.
 *
 * Built in one place because the screen needs it three times — to fetch, to
 * link the next page, and to keep the filters in the address so a view can be
 * shared and the browser's back button works.
 */
export function auditSearch(query: AuditQuery, limit = AUDIT_PAGE): URLSearchParams {
  const search = new URLSearchParams();
  if (query.actor) search.set("actor", query.actor);
  if (query.action) search.set("action", query.action);
  if (query.entity) search.set("entity", query.entity);
  if (query.entityId) search.set("entity_id", query.entityId);
  if (query.from) search.set("from", query.from);
  if (query.to) search.set("to", query.to);
  if (query.offset) search.set("offset", String(query.offset));
  search.set("limit", String(limit));
  return search;
}

/**
 * A day, as the filter inputs speak it, turned into the instant the API wants.
 *
 * `to` is exclusive on the server, so "up to and including the 3rd" is
 * midnight on the 4th. Doing that arithmetic in the component would put a
 * date's worth of off-by-one where nobody would look for it.
 */
export function dayBounds(from: string, to: string): { from?: string; to?: string } {
  const bounds: { from?: string; to?: string } = {};
  if (from) bounds.from = `${from}T00:00:00Z`;
  if (to) {
    const next = new Date(`${to}T00:00:00Z`);
    next.setUTCDate(next.getUTCDate() + 1);
    bounds.to = next.toISOString().replace(/\.\d{3}Z$/, "Z");
  }
  return bounds;
}
