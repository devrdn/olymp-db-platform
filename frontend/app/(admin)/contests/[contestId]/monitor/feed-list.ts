import type { FeedItem, FeedPage } from "@/lib/api/monitor";

/**
 * The live feed's list, as plain state transitions: the first page, a poll
 * for what is new, a read of older items, and the refresh of a query that
 * was still running when it arrived.
 *
 * The list is bounded. A contest of two hundred participants produces
 * thousands of items an hour, and an organiser's tab stays open all day; what
 * falls off the end is still in the API and is read again on request.
 */

/** The most items the screen holds at once. */
export const FEED_LIMIT = 1000;

export type FeedState = {
  /** Oldest first. */
  items: FeedItem[];
  /**
   * The cursor the next poll asks `after=`: the newest item ever delivered,
   * kept apart from `items` so the poll goes on while the list holds history.
   */
  newest?: string;
  /** Whether there is anything before `items[0]` to load. */
  olderAvailable: boolean;
  /**
   * The list no longer ends at the live tail: the organiser loaded older
   * items past the bound and the newest end was dropped to make room. New
   * items are then counted in `missed` rather than added, and going back to
   * the latest reads the newest page afresh.
   */
  detached: boolean;
  missed: number;
};

export function initialFeed(page: FeedPage): FeedState {
  return {
    items: page.items.slice(-FEED_LIMIT),
    newest: page.newest,
    olderAvailable: page.more || page.items.length > FEED_LIMIT,
    detached: false,
    missed: 0,
  };
}

/** The items of `incoming` that are not in `held`. */
function unseen(held: readonly FeedItem[], incoming: readonly FeedItem[]): FeedItem[] {
  if (incoming.length === 0) return [];
  const seen = new Set(held.map((item) => item.cursor));
  return incoming.filter((item) => !seen.has(item.cursor));
}

/**
 * A poll's page (`after=`), appended. `added` is what was new, for the rows
 * of the table that light up; an empty poll returns the very same state.
 */
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

/** A page read `before=` the oldest held item, put in front of it. */
export function prependOlder(state: FeedState, page: FeedPage): FeedState {
  const older = unseen(state.items, page.items);
  const joined = older.concat(state.items);
  const overflow = joined.length - FEED_LIMIT;
  return {
    ...state,
    items: overflow > 0 ? joined.slice(0, FEED_LIMIT) : joined,
    olderAvailable: page.more,
    detached: state.detached || overflow > 0,
  };
}

function isRunning(item: FeedItem): boolean {
  return item.detail.type === "query" && item.detail.status === "running";
}

function sameDetail(a: FeedItem, b: FeedItem): boolean {
  return JSON.stringify(a.detail) === JSON.stringify(b.detail);
}

/**
 * Items read again, put in place of the ones with the same cursor. Items the
 * list does not hold are ignored; a refresh that changed nothing returns the
 * very same state.
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

/** When each running query was last asked about, and how many times. */
export type RunningTries = Map<string, { at: number; count: number }>;

/**
 * How many times a running query is asked about before the screen leaves it
 * as it is: five minutes at the poll's cadence, far past any statement
 * timeout. A query whose journal row was never completed (its runner died
 * mid-statement) would otherwise cost a read every five seconds all day.
 */
export const MAX_RUNNING_TRIES = 60;

/**
 * Which running queries to ask about next, as one feed read: one
 * participant's, from the first of them to just past the last.
 *
 * A query is journalled as `running` before it runs and completed after, and
 * the feed does not deliver an item again once the cursor has passed it. So
 * the screen asks for it again by time — `from` is inclusive, `until`
 * exclusive, and the item's time is to the millisecond — narrowed to the
 * participant, whose queries in that window are few.
 *
 * One read per poll, never one per query: the participant chosen is the one
 * whose running query has waited longest since it was last asked about
 * (`tried`, updated here), so a query running for a minute does not starve
 * everybody else's.
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
  // Forget what is no longer on screen, so the map is bounded by the list.
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
