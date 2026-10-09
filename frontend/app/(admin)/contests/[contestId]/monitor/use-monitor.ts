"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import { useRouter } from "next/navigation";

import { ApiError } from "@/lib/api/client";
import {
  fetchFeed,
  fetchRoster,
  fetchTimeline,
  MAX_FEED_PAGE,
  type FeedPage,
  type FeedParams,
  type ReadOptions,
  type Roster,
} from "@/lib/api/monitor";

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

/** Poll interval while the tab is visible (SPEC.md §5.1). */
export const MONITOR_POLL_MS = 5_000;

/** Delay before the next catch-up page. */
const CATCH_UP_MS = 1_000;

/**
 * Catch-up pages read before skipping to the newest page. A tab hidden for an
 * hour can be thousands of items behind, and the list keeps only the last
 * thousand; skipped items remain in participants' pages and the CSV.
 */
export const MAX_CATCH_UP_PAGES = 5;

export const MAX_FAILURE_WAIT_MS = 60_000;

/** A tab turning visible this soon after a poll keeps the cadence. */
const FRESH_ENOUGH_MS = 2_000;

export const HIGHLIGHT_MS = 3_000;

export type MonitorProblem = { kind: "forbidden" } | { kind: "tooOften"; seconds: number } | { kind: "failed" } | null;

const NOBODY: ReadonlySet<string> = new Set();

/** `from` inclusive, `until` exclusive; either may be open. */
export type FeedRange = { from?: string; until?: string };

const NO_KINDS: string[] = [];

function rangeParams(range: FeedRange): FeedRange {
  return {
    ...(range.from ? { from: range.from } : {}),
    ...(range.until ? { until: range.until } : {}),
  };
}

/**
 * Live state of the monitoring screen: the participants table and the feed,
 * polled while the tab is visible. With `participant` the feed is that
 * participant's timeline, and without `roster` no table is read.
 *
 * A hidden tab reads nothing. Polls form one chain, scheduled after the
 * previous answer, so they never overlap. A 429 waits out `Retry-After`
 * (visibility does not cut it short); a 403 stops the chain. Quiet polls change
 * no state (`mergeRoster`). After the feed, at most one read refreshes one
 * participant's running queries.
 */
export function useMonitor({
  contestId,
  participant,
  roster: initialRoster,
  feed: initialPage,
  kinds: initialKinds = NO_KINDS,
}: {
  contestId: string;
  /** One participant's registration; the feed becomes their timeline. */
  participant?: string;
  /** The table to keep current; absent, none is read. */
  roster?: Roster;
  feed: FeedPage;
  kinds?: string[];
}) {
  const router = useRouter();
  const [rows, setRows] = useState(initialRoster?.rows ?? []);
  const [truncated, setTruncated] = useState(initialRoster?.truncated ?? false);
  const [feed, setFeedState] = useState<FeedState>(() => initialFeed(initialPage));
  const [kinds, setKindsState] = useState<string[]>(initialKinds);
  const [range, setRangeState] = useState<FeedRange>({});
  const [problem, setProblem] = useState<MonitorProblem>(null);
  const [fresh, setFresh] = useState<ReadonlySet<string>>(NOBODY);
  const [loadingOlder, setLoadingOlder] = useState(false);

  // Current values for asynchronous reads.
  const feedRef = useRef(feed);
  const kindsRef = useRef(kinds);
  const rangeRef = useRef(range);
  const withRoster = useRef(initialRoster !== undefined).current;
  // Bumped when the feed is replaced whole; an earlier read's answer is then
  // dropped.
  const generationRef = useRef(0);
  const triedRef = useRef<RunningTries>(new Map());
  const freshTokens = useRef(new Map<string, number>());
  const freshCounter = useRef(0);
  const freshTimers = useRef(new Set<ReturnType<typeof setTimeout>>());
  // A 429's wait, respected by every read.
  const quietUntilRef = useRef(0);
  // Consecutive failures for the back-off.
  const failuresRef = useRef(0);
  const catchUpRef = useRef({ pages: 0, items: 0 });
  // Aborted when the screen goes; owned by the polling effect, null before it
  // runs.
  const abortRef = useRef<AbortController | null>(null);

  const read = useCallback(
    (params: FeedParams, options: ReadOptions) =>
      participant
        ? fetchTimeline(contestId, participant, params, options)
        : fetchFeed(contestId, params, options),
    [contestId, participant],
  );

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
      // Only ids no later item has lit again go out.
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

  /** The wait before the next poll after a failure, or null to stop. */
  const failed = useCallback(
    (error: unknown): number | null => {
      if (abortRef.current?.signal.aborted) return null;
      if (error instanceof ApiError) {
        if (error.status === 429) {
          const seconds = error.retryAfterSeconds ?? MONITOR_POLL_MS / 1000;
          quietUntilRef.current = Date.now() + seconds * 1000;
          setProblem({ kind: "tooOften", seconds });
          return seconds * 1000;
        }
        if (error.status === 403) {
          setProblem({ kind: "forbidden" });
          return null;
        }
        if (error.status === 401) {
          // The layout redirects a lost session.
          router.refresh();
          return MONITOR_POLL_MS;
        }
      }
      // Exponential back-off to a ceiling, so many tabs do not hammer a failing
      // server.
      failuresRef.current += 1;
      setProblem({ kind: "failed" });
      return Math.min(MAX_FAILURE_WAIT_MS, MONITOR_POLL_MS * 2 ** (failuresRef.current - 1));
    },
    [router],
  );

  /** Reads the newest page; `gap` counts what is skipped. */
  const reload = useCallback(
    async (nextKinds: string[], gap = 0) => {
      const generation = ++generationRef.current;
      catchUpRef.current = { pages: 0, items: 0 };
      const page = await read(
        { kinds: nextKinds, ...rangeParams(rangeRef.current), limit: MAX_FEED_PAGE },
        { signal: abortRef.current?.signal },
      );
      if (generation !== generationRef.current) return;
      triedRef.current.clear();
      setFeed(initialFeed(page, gap));
    },
    [read, setFeed],
  );

  /**
   * One poll; returns the wait before the next, or null to stop. A normal poll
   * reads table and feed, lights rows with new items and refreshes running
   * queries. A page with more behind it starts a catch-up: feed-only reads a
   * second apart that light nothing, abandoned after `MAX_CATCH_UP_PAGES` for
   * the newest page.
   */
  const poll = useCallback(async (): Promise<number | null> => {
    const generation = generationRef.current;
    const kindsNow = kindsRef.current;
    const signal = abortRef.current?.signal;
    const catchingUp = catchUpRef.current.pages > 0;
    try {
      const feedRead = read(
        { after: feedRef.current.newest, kinds: kindsNow, ...rangeParams(rangeRef.current), limit: MAX_FEED_PAGE },
        { signal },
      );
      if (!catchingUp && withRoster) {
        const [roster] = await Promise.all([fetchRoster(contestId, { signal }), feedRead]);
        setRows((current) => mergeRoster(current, roster.rows));
        setTruncated(roster.truncated);
      }
      const page = await feedRead;

      if (generation === generationRef.current) {
        const { state, added } = appendNewer(feedRef.current, page);
        if (state !== feedRef.current) setFeed(state);
        if (!catchingUp && !page.more) light([...new Set(added.map((item) => item.registrationId))]);
        if (page.more) {
          catchUpRef.current = {
            pages: catchUpRef.current.pages + 1,
            items: catchUpRef.current.items + page.items.length,
          };
        }
      }

      if (page.more && catchUpRef.current.pages > MAX_CATCH_UP_PAGES) {
        await reload(kindsNow, catchUpRef.current.items);
      } else if (page.more) {
        failuresRef.current = 0;
        setProblem(null);
        return CATCH_UP_MS;
      } else {
        catchUpRef.current = { pages: 0, items: 0 };
      }

      if (!catchingUp) {
        const window = runningWindow(feedRef.current.items, triedRef.current, Date.now());
        if (window) {
          const refreshed = await read(
            {
              participant: window.participant,
              kinds: ["query"],
              from: window.from,
              until: window.until,
              limit: MAX_FEED_PAGE,
            },
            { signal },
          );
          if (generation === generationRef.current) {
            const state = refreshItems(feedRef.current, refreshed.items);
            if (state !== feedRef.current) setFeed(state);
          }
        }
      }
      failuresRef.current = 0;
      setProblem(null);
      return MONITOR_POLL_MS;
    } catch (error: unknown) {
      return failed(error);
    }
  }, [contestId, failed, light, read, reload, setFeed, withRoster]);

  // Read through a ref so a re-render never restarts the chain, which would
  // forget a 429's wait or a 403's stop.
  const pollRef = useRef(poll);
  useEffect(() => {
    pollRef.current = poll;
  }, [poll]);

  useEffect(() => {
    let cancelled = false;
    let stopped = false;
    let inFlight = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let lastFinished = 0;
    const controller = new AbortController();
    abortRef.current = controller;

    const visible = () => document.visibilityState !== "hidden";

    const schedule = (ms: number) => {
      clearTimeout(timer);
      timer = setTimeout(tick, ms);
    };

    const tick = async () => {
      timer = undefined;
      if (cancelled || stopped || inFlight || !visible()) return;
      // Another read may have been refused since this tick was scheduled.
      const quiet = quietUntilRef.current - Date.now();
      if (quiet > 0) {
        schedule(quiet);
        return;
      }
      inFlight = true;
      const wait = await pollRef.current();
      inFlight = false;
      lastFinished = Date.now();
      if (cancelled) return;
      if (wait === null) {
        stopped = true;
        return;
      }
      if (visible()) schedule(wait);
    };

    const onVisibility = () => {
      if (!visible()) {
        clearTimeout(timer);
        timer = undefined;
        return;
      }
      if (stopped || inFlight) return;
      // If the last poll is still fresh, keep the cadence instead of reading
      // again.
      const sinceLast = Date.now() - lastFinished;
      const wait = sinceLast < FRESH_ENOUGH_MS ? MONITOR_POLL_MS - sinceLast : 0;
      schedule(Math.max(wait, quietUntilRef.current - Date.now()));
    };

    document.addEventListener("visibilitychange", onVisibility);
    schedule(MONITOR_POLL_MS);
    return () => {
      cancelled = true;
      controller.abort();
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [contestId, participant]);

  /** Reads the newest page, for a new filter or a jump to the latest. */
  const restart = useCallback(
    async (nextKinds: string[]) => {
      try {
        await reload(nextKinds);
      } catch (error: unknown) {
        failed(error);
      }
    },
    [failed, reload],
  );

  const setKinds = useCallback(
    async (next: string[]) => {
      kindsRef.current = next;
      setKindsState(next);
      await restart(next);
    },
    [restart],
  );

  /** Narrows every read to a time range and reloads. */
  const setRange = useCallback(
    async (next: FeedRange) => {
      rangeRef.current = next;
      setRangeState(next);
      await restart(kindsRef.current);
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
      const page = await read(
        {
          before: current.items[0].cursor,
          kinds: kindsRef.current,
          ...rangeParams(rangeRef.current),
          limit: MAX_FEED_PAGE,
        },
        { signal: abortRef.current?.signal },
      );
      if (generation === generationRef.current) setFeed(prependOlder(feedRef.current, page));
    } catch (error: unknown) {
      failed(error);
    } finally {
      setLoadingOlder(false);
    }
  }, [failed, read, setFeed]);

  return {
    rows,
    truncated,
    feed,
    fresh,
    problem,
    kinds,
    setKinds,
    range,
    setRange,
    loadOlder,
    loadingOlder,
    toLatest,
  };
}
