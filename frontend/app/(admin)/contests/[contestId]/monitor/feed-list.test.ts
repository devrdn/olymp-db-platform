import { describe, expect, test } from "vitest";

import type { FeedItem, FeedPage } from "@/lib/api/monitor";

import {
  appendNewer,
  FEED_LIMIT,
  initialFeed,
  MAX_RUNNING_TRIES,
  prependOlder,
  refreshItems,
  runningWindow,
  type RunningTries,
} from "./feed-list";
import { feedItem } from "./test-fixtures";

function page(items: FeedItem[], more = false): FeedPage {
  return {
    items,
    more,
    newest: items.at(-1)?.cursor,
    oldest: items[0]?.cursor,
  };
}

function items(from: number, to: number): FeedItem[] {
  const out: FeedItem[] = [];
  for (let i = from; i <= to; i += 1) out.push(feedItem(`c${i}`));
  return out;
}

function running(cursor: string, registrationId: string, at: string): FeedItem {
  const item = feedItem(cursor, { registrationId, at });
  return { ...item, detail: { ...(item.detail as Extract<FeedItem["detail"], { type: "query" }>), status: "running", durationMs: null } };
}

describe("the feed's first page", () => {
  test("holds the page and polls after its newest item", () => {
    const state = initialFeed(page(items(1, 3), true));

    expect(state.items.map((i) => i.cursor)).toEqual(["c1", "c2", "c3"]);
    expect(state.newest).toBe("c3");
    expect(state.olderAvailable).toBe(true);
    expect(state.detached).toBe(false);
  });

  test("an empty contest polls from the start", () => {
    const state = initialFeed(page([]));
    expect(state.items).toEqual([]);
    expect(state.newest).toBeUndefined();
  });
});

describe("a poll for what is new", () => {
  test("appends in order and moves the cursor", () => {
    const { state, added } = appendNewer(initialFeed(page(items(1, 2))), page(items(3, 4)));

    expect(state.items.map((i) => i.cursor)).toEqual(["c1", "c2", "c3", "c4"]);
    expect(added.map((i) => i.cursor)).toEqual(["c3", "c4"]);
    expect(state.newest).toBe("c4");
  });

  /** A poll racing a reset, or a duplicated response, must not show a line twice. */
  test("never shows an item twice", () => {
    const first = initialFeed(page(items(1, 3)));
    const { state, added } = appendNewer(first, page(items(2, 5)));

    expect(state.items.map((i) => i.cursor)).toEqual(["c1", "c2", "c3", "c4", "c5"]);
    expect(added.map((i) => i.cursor)).toEqual(["c4", "c5"]);
  });

  test("an empty poll changes nothing at all", () => {
    const first = initialFeed(page(items(1, 3)));
    const { state, added } = appendNewer(first, page([]));

    expect(state).toBe(first);
    expect(added).toEqual([]);
  });

  test("keeps no more than the bound, dropping the oldest", () => {
    const first = initialFeed(page(items(1, FEED_LIMIT)));
    const { state } = appendNewer(first, page(items(FEED_LIMIT + 1, FEED_LIMIT + 150)));

    expect(state.items).toHaveLength(FEED_LIMIT);
    expect(state.items[0].cursor).toBe("c151");
    expect(state.items.at(-1)?.cursor).toBe(`c${FEED_LIMIT + 150}`);
    // Dropped items can be read again.
    expect(state.olderAvailable).toBe(true);
  });
});

describe("loading older items", () => {
  test("puts them before the oldest held", () => {
    const first = initialFeed(page(items(5, 6), true));
    const state = prependOlder(first, page(items(3, 4), false));

    expect(state.items.map((i) => i.cursor)).toEqual(["c3", "c4", "c5", "c6"]);
    expect(state.olderAvailable).toBe(false);
    expect(state.detached).toBe(false);
  });

  /** The skipped-items note is stale once older items are read. */
  test("clears the note about skipped items once older ones are read", () => {
    const first = initialFeed(page(items(5, 6), true), 1200);
    expect(prependOlder(first, page(items(3, 4), true)).gap).toBe(0);
  });

  /** Past the bound the newest end is dropped and new items are counted until the jump back. */
  test("past the bound, drops the newest end and stops following", () => {
    const first = initialFeed(page(items(201, 200 + FEED_LIMIT), true));
    const state = prependOlder(first, page(items(1, 200), true));

    expect(state.items).toHaveLength(FEED_LIMIT);
    expect(state.items[0].cursor).toBe("c1");
    expect(state.detached).toBe(true);
    expect(state.newest).toBe(`c${200 + FEED_LIMIT}`);

    const { state: polled, added } = appendNewer(state, page(items(2000, 2002)));
    expect(polled.items).toBe(state.items);
    expect(added).toHaveLength(3);
    expect(polled.missed).toBe(3);
    expect(polled.newest).toBe("c2002");
  });
});

/**
 * A query first arrives as `running` and the cursor never returns to it, so it
 * is re-read by time.
 */
describe("queries still running", () => {
  test("asks for one participant's running queries by their time", () => {
    const state = initialFeed(
      page([
        running("c1", "p1", "2026-09-20T10:00:01.100Z"),
        feedItem("c2"),
        running("c3", "p2", "2026-09-20T10:00:02.000Z"),
        running("c4", "p1", "2026-09-20T10:00:03.500Z"),
      ]),
    );
    const tried: RunningTries = new Map();

    const window = runningWindow(state.items, tried, 1000);
    expect(window).toEqual({
      participant: "p1",
      from: "2026-09-20T10:00:01.100Z",
      until: "2026-09-20T10:00:03.501Z",
    });

    // Whoever waited longest goes next, so none starves.
    expect(runningWindow(state.items, tried, 2000)).toMatchObject({ participant: "p2" });
    expect(runningWindow(state.items, tried, 3000)).toMatchObject({ participant: "p1" });
  });

  /** A query whose runner died stays `running`; it is asked about a while, then left. */
  test("gives up on a query that never ends", () => {
    const items = [running("c1", "p1", "2026-09-20T10:00:01.100Z")];
    const tried: RunningTries = new Map();

    for (let i = 0; i < MAX_RUNNING_TRIES; i += 1) expect(runningWindow(items, tried, i)).not.toBeNull();
    expect(runningWindow(items, tried, MAX_RUNNING_TRIES)).toBeNull();
  });

  test("asks nothing when nothing is running", () => {
    expect(runningWindow(items(1, 3), new Map(), 0)).toBeNull();
  });

  test("replaces a running query with its ending and leaves the rest alone", () => {
    const state = initialFeed(page([running("c1", "p1", "2026-09-20T10:00:01.100Z"), feedItem("c2")]));
    const done = feedItem("c1", { registrationId: "p1", at: "2026-09-20T10:00:01.100Z" });

    const next = refreshItems(state, [done, feedItem("c9")]);

    expect(next.items[0]).toBe(done);
    expect(next.items[1]).toBe(state.items[1]);
    expect(next.items).toHaveLength(2);
  });

  test("a refresh that finds it still running changes nothing", () => {
    const state = initialFeed(page([running("c1", "p1", "2026-09-20T10:00:01.100Z")]));
    expect(refreshItems(state, [running("c1", "p1", "2026-09-20T10:00:01.100Z")])).toBe(state);
  });
});
