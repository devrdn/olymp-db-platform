import type { FeedItem, FeedPage } from "@/lib/api/monitor";

/**
 * The live feed's list as pure state transitions: first page, polls for newer
 * items, reads of older ones, and refreshes of running queries. Bounded, since
 * an open tab collects thousands of items an hour; dropped items can be read
 * again from the API.
 */

export const FEED_LIMIT = 1000;

export type FeedState = {
  items: FeedItem[];
  /**
   * The `after=` cursor: the newest item ever delivered, kept apart from
   * `items` so polling continues while the list holds history.
   */
  newest?: string;
  olderAvailable: boolean;
  /**
   * Loading older items past the bound dropped the newest end. New items are
   * then counted in `missed`, and returning to the latest reloads the newest
   * page.
   */
  detached: boolean;
  missed: number;
  /**
   * A lower bound on items skipped when the screen fell too far behind and
   * reloaded the newest page; zero otherwise.
   */
  gap: number;
};

export function initialFeed(page: FeedPage, gap = 0): FeedState {
  return {
    items: page.items.slice(-FEED_LIMIT),
    newest: page.newest,
    olderAvailable: page.more || page.items.length > FEED_LIMIT,
    detached: false,
    missed: 0,
    gap,
  };
}

function unseen(held: readonly FeedItem[], incoming: readonly FeedItem[]): FeedItem[] {
  if (incoming.length === 0) return [];
  const seen = new Set(held.map((item) => item.cursor));
  return incoming.filter((item) => !seen.has(item.cursor));
}

/** Appends a poll's page. `added` lights table rows; an empty poll returns the same state. */
export function appendNewer(state: FeedState, page: FeedPage): { state: FeedState; added: FeedItem[] } {
  const added = unseen(state.items, page.items);
  const newest = page.newest ?? state.newest;
  if (added.length === 0 && newest === state.newest) return { state, added };
  if (state.detached) {
    return { state: { ...state, newest, missed: state.missed + added.length }, added };
  }
  const joined = state.items.concat(added);
  const overflow = joined.length - FEED_LIMIT;
  return {
    state: {
      ...state,
      items: overflow > 0 ? joined.slice(overflow) : joined,
      newest,
      olderAvailable: state.olderAvailable || overflow > 0,
    },
    added,
  };
}

/** Prepends a page read `before=` the oldest held item. */
export function prependOlder(state: FeedState, page: FeedPage): FeedState {
  const older = unseen(state.items, page.items);
  const joined = older.concat(state.items);
  const overflow = joined.length - FEED_LIMIT;
  return {
    ...state,
    items: overflow > 0 ? joined.slice(0, FEED_LIMIT) : joined,
    olderAvailable: page.more,
    detached: state.detached || overflow > 0,
    // Reading into the skipped stretch makes the count stale.
    gap: 0,
  };
}

function isRunning(item: FeedItem): boolean {
  return item.detail.type === "query" && item.detail.status === "running";
}

function sameDetail(a: FeedItem, b: FeedItem): boolean {
  return JSON.stringify(a.detail) === JSON.stringify(b.detail);
}

/**
 * Replaces items with the same cursor; unknown items are ignored and an
 * unchanged refresh returns the same state.
 */
export function refreshItems(state: FeedState, fresh: readonly FeedItem[]): FeedState {
  if (fresh.length === 0) return state;
  const byCursor = new Map(fresh.map((item) => [item.cursor, item]));
  let changed = false;
  const items = state.items.map((item) => {
    const next = byCursor.get(item.cursor);
    if (!next || sameDetail(item, next)) return item;
    changed = true;
    return next;
  });
  return changed ? { ...state, items } : state;
}

export type RunningWindow = { participant: string; from: string; until: string };

export type RunningTries = Map<string, { at: number; count: number }>;

/**
 * Tries before a running query is left alone: five minutes at the poll cadence,
 * past any statement timeout. A query whose runner died would otherwise cost a
 * read every five seconds all day.
 */
export const MAX_RUNNING_TRIES = 60;

/**
 * The next running-queries read: one participant's, from their first running
 * query to just past the last. A query is logged as `running` before it runs
 * and the cursor never returns to it, so it is re-read by time (`from`
 * inclusive, `until` exclusive, millisecond precision). One read per poll, for
 * the participant whose running query has waited longest (`tried`, updated
 * here), so none starves.
 */
export function runningWindow(items: readonly FeedItem[], tried: RunningTries, now: number): RunningWindow | null {
  const live = (item: FeedItem) => isRunning(item) && (tried.get(item.cursor)?.count ?? 0) < MAX_RUNNING_TRIES;

  let pick: FeedItem | undefined;
  let pickTried = Infinity;
  for (const item of items) {
    if (!live(item)) continue;
    const last = tried.get(item.cursor)?.at ?? -Infinity;
    if (last < pickTried) {
      pick = item;
      pickTried = last;
    }
  }
  if (!pick) return null;

  let from = pick.at;
  let until = pick.at;
  for (const item of items) {
    if (item.registrationId !== pick.registrationId || !live(item)) continue;
    tried.set(item.cursor, { at: now, count: (tried.get(item.cursor)?.count ?? 0) + 1 });
    if (item.at < from) from = item.at;
    if (item.at > until) until = item.at;
  }
  // Bound the map by the list.
  if (tried.size > FEED_LIMIT) {
    const held = new Set(items.map((item) => item.cursor));
    for (const cursor of tried.keys()) if (!held.has(cursor)) tried.delete(cursor);
  }
  return {
    participant: pick.registrationId,
    from,
    until: new Date(Date.parse(until) + 1).toISOString(),
  };
}
