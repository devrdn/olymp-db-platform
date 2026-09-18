"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import { useRouter } from "next/navigation";

import { ApiError } from "@/lib/api/client";
import { fetchFeed, fetchRoster, MAX_FEED_PAGE, type FeedPage, type Roster } from "@/lib/api/monitor";

import {
  appendNewer,
  initialFeed,
  prependOlder,
  refreshItems,
  runningWindow,
  type FeedState,
  type RunningTries,
} from "./feed-list";
import { mergeRoster } from "./roster";

/** How often the table and the feed are asked again while the tab is visible (design §4). */
export const MONITOR_POLL_MS = 5_000;

/** How soon to ask again when a poll's page said there is more past it. */
const CATCH_UP_MS = 1_000;

/** How long a participant's row stays lit after a new item of theirs. */
export const HIGHLIGHT_MS = 3_000;

export type MonitorProblem = { kind: "forbidden" } | { kind: "tooOften"; seconds: number } | { kind: "failed" } | null;

const NOBODY: ReadonlySet<string> = new Set();

/**
 * The monitoring screen's live state: the participants table and the feed,
 * both asked again every five seconds while the tab is visible.
 *
 * - **Hidden, nothing.** A hidden tab asks nothing; becoming visible asks at
 *   once, then keeps the cadence. Forty organisers' tabs in the background
 *   cost the API nothing.
 * - **One chain, never overlapping.** The next poll is scheduled when the
 *   last one has answered, so a slow answer is never raced by the next.
 * - **Refusals.** A 429 waits out its `Retry-After` — a tab turning visible
 *   does not cut the wait short — and a 403 stops the chain: the permission
 *   is gone, and asking every five seconds will not bring it back.
 * - **Nothing changed, nothing renders.** The table is merged row by row
 *   (`mergeRoster`) and an empty feed page returns the same state, so a
 *   quiet poll moves no state at all.
 * - **Running queries.** After the feed, one more read at most asks for the
 *   running queries of one participant again (`runningWindow`); the
 *   finished query takes the running one's place.
 */
export function useMonitor({
  contestId,
  roster: initialRoster,
  feed: initialPage,
}: {
  contestId: string;
  roster: Roster;
  feed: FeedPage;
}) {
  const router = useRouter();
  const [rows, setRows] = useState(initialRoster.rows);
  const [truncated, setTruncated] = useState(initialRoster.truncated);
  const [feed, setFeedState] = useState<FeedState>(() => initialFeed(initialPage));
  const [kinds, setKindsState] = useState<string[]>([]);
  const [problem, setProblem] = useState<MonitorProblem>(null);
  const [fresh, setFresh] = useState<ReadonlySet<string>>(NOBODY);
  const [loadingOlder, setLoadingOlder] = useState(false);

  // What the asynchronous reads need to see as it is now, not as it was when
  // the effect that started them ran.
  const feedRef = useRef(feed);
  const kindsRef = useRef(kinds);
  // Bumped whenever the feed is replaced whole (a new filter, a jump back to
  // the latest): a read begun before that answers for a list that is gone.
  const generationRef = useRef(0);
  const triedRef = useRef<RunningTries>(new Map());
  const freshTokens = useRef(new Map<string, number>());
  const freshCounter = useRef(0);
  const freshTimers = useRef(new Set<ReturnType<typeof setTimeout>>());

  const setFeed = useCallback((next: FeedState) => {
    feedRef.current = next;
    setFeedState(next);
  }, []);

  const light = useCallback((ids: string[]) => {
    if (ids.length === 0) return;
    const token = ++freshCounter.current;
    for (const id of ids) freshTokens.current.set(id, token);
    setFresh((current) => {
      if (ids.every((id) => current.has(id))) return current;
      return new Set([...current, ...ids]);
    });
    const timer = setTimeout(() => {
      freshTimers.current.delete(timer);
      // Only the ids no later item has lit again go out.
      const out = ids.filter((id) => freshTokens.current.get(id) === token);
      for (const id of out) freshTokens.current.delete(id);
      if (out.length === 0) return;
      setFresh((current) => {
        const next = new Set(current);
        for (const id of out) next.delete(id);
        return next.size === 0 ? NOBODY : next;
      });
    }, HIGHLIGHT_MS);
    freshTimers.current.add(timer);
  }, []);

  useEffect(() => {
    const timers = freshTimers.current;
    return () => {
      for (const timer of timers) clearTimeout(timer);
    };
  }, []);

  /** What a failed read means for the chain: the wait before the next, or null to stop. */
  const failed = useCallback(
    (error: unknown): number | null => {
      if (error instanceof ApiError) {
        if (error.status === 429) {
          const seconds = error.retryAfterSeconds ?? MONITOR_POLL_MS / 1000;
          setProblem({ kind: "tooOften", seconds });
          return seconds * 1000;
        }
        if (error.status === 403) {
          setProblem({ kind: "forbidden" });
          return null;
        }
        if (error.status === 401) {
          // The layout above knows where a lost session goes.
          router.refresh();
          return MONITOR_POLL_MS;
        }
      }
      setProblem({ kind: "failed" });
      return MONITOR_POLL_MS;
    },
    [router],
  );

  /** One poll; answers the wait before the next, or null to stop. */
  const poll = useCallback(async (): Promise<number | null> => {
    const generation = generationRef.current;
    const kindsNow = kindsRef.current;
    try {
      const [roster, page] = await Promise.all([
        fetchRoster(contestId),
        fetchFeed(contestId, { after: feedRef.current.newest, kinds: kindsNow, limit: MAX_FEED_PAGE }),
      ]);
      setRows((current) => mergeRoster(current, roster.rows));
      setTruncated(roster.truncated);
      if (generation === generationRef.current) {
        const { state, added } = appendNewer(feedRef.current, page);
        if (state !== feedRef.current) setFeed(state);
        light([...new Set(added.map((item) => item.registrationId))]);
      }

      const window = runningWindow(feedRef.current.items, triedRef.current, Date.now());
      if (window) {
        const refreshed = await fetchFeed(contestId, {
          participant: window.participant,
          kinds: ["query"],
          from: window.from,
          until: window.until,
          limit: MAX_FEED_PAGE,
        });
        if (generation === generationRef.current) {
          const state = refreshItems(feedRef.current, refreshed.items);
          if (state !== feedRef.current) setFeed(state);
        }
      }
      setProblem(null);
      return page.more ? CATCH_UP_MS : MONITOR_POLL_MS;
    } catch (error: unknown) {
      return failed(error);
    }
  }, [contestId, failed, light, setFeed]);

  // The chain below reads the poll through a ref, so that nothing a render
  // brings — a new router object, a new callback — restarts it: a restarted
  // chain would forget a 429's wait and a 403's stop.
  const pollRef = useRef(poll);
  useEffect(() => {
    pollRef.current = poll;
  }, [poll]);

  useEffect(() => {
    let cancelled = false;
    let stopped = false;
    let inFlight = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    // A 429's wait, which a tab turning visible does not cut short.
    let quietUntil = 0;

    const visible = () => document.visibilityState !== "hidden";

    const schedule = (ms: number) => {
      clearTimeout(timer);
      timer = setTimeout(tick, ms);
    };

    const tick = async () => {
      timer = undefined;
      if (cancelled || stopped || inFlight || !visible()) return;
      inFlight = true;
      const wait = await pollRef.current();
      inFlight = false;
      if (cancelled) return;
      if (wait === null) {
        stopped = true;
        return;
      }
      if (wait > MONITOR_POLL_MS) quietUntil = Date.now() + wait;
      if (visible()) schedule(wait);
    };

    const onVisibility = () => {
      if (!visible()) {
        clearTimeout(timer);
        timer = undefined;
        return;
      }
      if (stopped || inFlight) return;
      schedule(Math.max(0, quietUntil - Date.now()));
    };

    document.addEventListener("visibilitychange", onVisibility);
    schedule(MONITOR_POLL_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [contestId]);

  /** Reads the newest page afresh, for a new filter or a jump back to the latest. */
  const restart = useCallback(
    async (nextKinds: string[]) => {
      const generation = ++generationRef.current;
      try {
        const page = await fetchFeed(contestId, { kinds: nextKinds, limit: MAX_FEED_PAGE });
        if (generation !== generationRef.current) return;
        triedRef.current.clear();
        setFeed(initialFeed(page));
      } catch (error: unknown) {
        failed(error);
      }
    },
    [contestId, failed, setFeed],
  );

  const setKinds = useCallback(
    async (next: string[]) => {
      kindsRef.current = next;
      setKindsState(next);
      await restart(next);
    },
    [restart],
  );

  const toLatest = useCallback(() => restart(kindsRef.current), [restart]);

  const loadOlder = useCallback(async () => {
    const current = feedRef.current;
    if (!current.olderAvailable || current.items.length === 0) return;
    const generation = generationRef.current;
    setLoadingOlder(true);
    try {
      const page = await fetchFeed(contestId, {
        before: current.items[0].cursor,
        kinds: kindsRef.current,
        limit: MAX_FEED_PAGE,
      });
      if (generation === generationRef.current) setFeed(prependOlder(feedRef.current, page));
    } catch (error: unknown) {
      failed(error);
    } finally {
      setLoadingOlder(false);
    }
  }, [contestId, failed, setFeed]);

  return { rows, truncated, feed, fresh, problem, kinds, setKinds, loadOlder, loadingOlder, toLatest };
}
