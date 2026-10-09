import { z } from "zod";

/**
 * The trail of who did what, from where and when. Read-only: entries are
 * written in the same transaction as their action, never over HTTP.
 */

export const auditEntrySchema = z.object({
  id: z.number(),
  actor_id: z.string().optional(),
  /** Empty for a system event or an account deleted since. */
  actor_login: z.string().optional(),
  action: z.string(),
  entity: z.string().optional(),
  entity_id: z.string().optional(),
  /** Absent once the entity no longer exists; `entity_id` remains. */
  entity_label: z.string().optional(),
  payload: z.record(z.string(), z.unknown()).optional(),
  ip: z.string().optional(),
  user_agent: z.string().optional(),
  created_at: z.string(),
});

export const auditPageSchema = z.object({
  items: z.array(auditEntrySchema),
  total: z.number(),
});

/**
 * Every filterable action, from `audit.Actions()`. Fetched, not collected from
 * the rows on screen, which would offer only actions already shown.
 */
export const auditActionsSchema = z.object({ items: z.array(z.string()) });

export type AuditEntry = z.infer<typeof auditEntrySchema>;

/** Each filter is backed by an index on the table. */
export type AuditQuery = {
  actor?: string;
  action?: string;
  entity?: string;
  entityId?: string;
  from?: string;
  to?: string;
  offset?: number;
};

export const AUDIT_PAGE = 50;

/** The query string for a page of the trail, used to fetch, to link pages, and in the address. */
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
 * Filter days as API instants. `to` is exclusive on the server, so "up to and
 * including the 3rd" is midnight on the 4th.
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

export type FieldChange = { field: string; from: string; to: string };

/**
 * What an entry says it changed. The server records only fields that moved,
 * and marks an entry where none did, which is shown as such
 * (docs/ARCHITECTURE.md §9.2).
 */
export function summariseChanges(
  payload: Record<string, unknown> | undefined,
): { changes: FieldChange[]; unchanged: boolean } {
  if (payload?.changed === false) return { changes: [], unchanged: true };

  const raw = payload?.changes;
  if (!raw || typeof raw !== "object") return { changes: [], unchanged: false };

  const changes = Object.entries(raw as Record<string, unknown>)
    .map(([field, value]) => {
      const pair = (value ?? {}) as { from?: unknown; to?: unknown };
      return { field, from: renderValue(pair.from), to: renderValue(pair.to) };
    })
    // The server sends a map; sort so rows do not reshuffle between reloads.
    .sort((a, b) => a.field.localeCompare(b.field));

  return { changes, unchanged: false };
}

/**
 * The publish-gate problem codes a `contest.start_blocked` entry carries,
 * worded by `workspace.gate.problems`. Other entries have none.
 */
export function blockedProblems(payload: Record<string, unknown> | undefined): string[] {
  const raw = payload?.problems;
  if (!Array.isArray(raw)) return [];
  return raw.filter((code): code is string => typeof code === "string");
}

/**
 * The `reason` codes of an `auth.login_failed` entry (`auth.Reason*`). A wrong
 * password and an unknown login both record `invalid_credentials`, because the
 * endpoint does not distinguish them either (docs/ARCHITECTURE.md §7.2);
 * `account_blocked` is disclosed only after the password matched. No generated
 * contract publishes this list, so `dictionary.test.ts` checks against it.
 */
export const LOGIN_FAILURE_REASONS = {
  invalidCredentials: "invalid_credentials",
  accountBlocked: "account_blocked",
  tooManyAttempts: "too_many_attempts",
} as const;

/** The reason an `auth.login_failed` entry carries, if any. */
export function loginFailureReason(payload: Record<string, unknown> | undefined): string | undefined {
  const reason = payload?.reason;
  return typeof reason === "string" ? reason : undefined;
}

function renderValue(value: unknown): string {
  if (value === null || value === undefined) return "—";
  if (Array.isArray(value)) return value.length === 0 ? "—" : value.join(", ");
  if (typeof value === "object") {
    const omitted = (value as { omitted_bytes?: unknown }).omitted_bytes;
    // The server replaces a value too large to keep with a note of its size.
    if (typeof omitted === "number") return `${omitted} B`;
    return JSON.stringify(value);
  }
  return String(value);
}
