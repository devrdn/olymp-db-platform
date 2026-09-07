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
  /**
   * The name of the thing acted upon, while it still exists. Absent once it
   * does not — the trail outlives what it describes, and the identifier is
   * what remains.
   */
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
 * The vocabulary the trail can be filtered by, as the server publishes it.
 *
 * Fetched rather than built from the page in hand. Actions are declared in
 * the domain (`audit.Actions()`), and a list built by scanning the rows on
 * screen can only ever offer an action that has already happened to be shown
 * — a filter that can only find what has already been found is not a filter.
 */
export const auditActionsSchema = z.object({ items: z.array(z.string()) });

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

/** One field an action moved, ready to render. */
export type FieldChange = { field: string; from: string; to: string };

/**
 * What an entry says it changed.
 *
 * The server records only fields that actually moved, and says so explicitly
 * when none did — "saved, nothing moved" is a fact worth showing, because
 * otherwise it is indistinguishable from a real edit that the reader simply
 * cannot see (architecture §9.2).
 *
 * Values are rendered rather than typed: the trail carries strings, numbers,
 * lists and the occasional note that a value was too large to keep, and this
 * is a column in a register, not a form.
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
    // A stable order, because the server sends a map and a register that
    // reshuffles its own rows between reloads is unreadable.
    .sort((a, b) => a.field.localeCompare(b.field));

  return { changes, unchanged: false };
}

/**
 * The problem codes a `contest.start_blocked` entry carries
 * (`backend/internal/contests/schedule.go`'s `startBlockedEntry`) — the same
 * closed vocabulary the publish gate's own screen already has wording for
 * (`workspace.gate.problems`), read back here rather than left in the raw
 * payload. An entry that is not a block, or one written before this field
 * existed, simply has none.
 */
export function blockedProblems(payload: Record<string, unknown> | undefined): string[] {
  const raw = payload?.problems;
  if (!Array.isArray(raw)) return [];
  return raw.filter((code): code is string => typeof code === "string");
}

/**
 * The closed vocabulary an `auth.login_failed` entry's `reason` can carry
 * (`backend/internal/auth/service.go`'s `Reason*` constants).
 *
 * Three codes, matching exactly what the login endpoint itself ever
 * distinguishes (architecture §7.2): a wrong password and a login that does
 * not exist both record `invalidCredentials`, because the endpoint answers
 * them identically and at the same time — recording which one happened would
 * put in a year-old, widely-read table a distinction the wire deliberately
 * erases. `accountBlocked` is safe to record because the endpoint itself only
 * ever discloses it after the same password matched, so a reader learns
 * nothing the caller was not already told. The same closed-list treatment as
 * `PUBLISH_PROBLEMS` below, and for the same reason: nothing on the backend
 * publishes this vocabulary as a generated contract yet, so this list is what
 * `dictionary.test.ts` checks the dictionaries against.
 */
export const LOGIN_FAILURE_REASONS = {
  invalidCredentials: "invalid_credentials",
  accountBlocked: "account_blocked",
  tooManyAttempts: "too_many_attempts",
} as const;

/**
 * The reason an `auth.login_failed` entry carries, read back from its
 * payload. An entry with no reason (written before this field existed, or
 * not a login failure at all) simply has none.
 */
export function loginFailureReason(payload: Record<string, unknown> | undefined): string | undefined {
  const reason = payload?.reason;
  return typeof reason === "string" ? reason : undefined;
}

/** The shortest honest rendering of a recorded value. */
function renderValue(value: unknown): string {
  if (value === null || value === undefined) return "—";
  if (Array.isArray(value)) return value.length === 0 ? "—" : value.join(", ");
  if (typeof value === "object") {
    const omitted = (value as { omitted_bytes?: unknown }).omitted_bytes;
    // The server replaces a value too large to keep with a note of its size.
    // Saying so is the point; pretending it was empty would be a lie.
    if (typeof omitted === "number") return `${omitted} B`;
    return JSON.stringify(value);
  }
  return String(value);
}
