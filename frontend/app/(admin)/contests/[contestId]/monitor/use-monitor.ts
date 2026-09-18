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

/** How often the table and the feed are asked again while the tab is visible (design §4). */
export const MONITOR_POLL_MS = 5_000;

/** How soon to ask again when a poll's page said there is more past it. */
const CATCH_UP_MS = 1_000;

/**
 * How many pages past the first a poll reads to catch up before it gives the
 * gap up and reads the newest page instead. A tab hidden for an hour can be
 * thousands of items behind; paging through them spends the read budget on a
 * list that keeps only the last thousand anyway. What was skipped is in the
 * participants' own pages and the CSV, and the table counts it all.
 */
export const MAX_CATCH_UP_PAGES = 5;

/** The longest wait between polls while the server keeps failing. */
export const MAX_FAILURE_WAIT_MS = 60_000;

/** A tab turning visible this soon after a poll finished waits for the cadence. */
const FRESH_ENOUGH_MS = 2_000;

/** How long a participant's row stays lit after a new item of theirs. */
export const HIGHLIGHT_MS = 3_000;

export type MonitorProblem = { kind: "forbidden" } | { kind: "tooOften"; seconds: number } | { kind: "failed" } | null;

const NOBODY: ReadonlySet<string> = new Set();

/** A stretch of time the feed is narrowed to: `from` inclusive, `until` exclusive, either open. */
export type FeedRange = { from?: string; until?: string };

const NO_KINDS: string[] = [];

/** The range as read parameters, naming only the ends that are set. */
function rangeParams(range: FeedRange): FeedRange {
  return {
    ...(range.from ? { from: range.from } : {}),
    ...(range.until ? { until: range.until } : {}),
  };
}

/**
 * The monitoring screen's live state: the participants table and the feed,
 * both asked again every five seconds while the tab is visible.
 *
 * One participant's page uses the same state with `participant` set: the
 * feed is that participant's timeline, and without a `roster` there is no
 * table to ask. `kinds` is the filter it opens with, and `setRange` narrows
 * every read to a stretch of time.
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
  participant,
  roster: initialRoster,
  feed: initialPage,
  kinds: initialKinds = NO_KINDS,
}: {
  contestId: string;
  /** One participant's registration: the feed is their timeline. */
  participant?: string;
  /** The table to keep current; absent, no table is asked. */
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

  // What the asynchronous reads need to see as it is now, not as it was when
  // the effect that started them ran.
  const feedRef = useRef(feed);
  const kindsRef = useRef(kinds);
  const rangeRef = useRef(range);
  // Whether there is a table at all; fixed for the screen's life.
  const withRoster = useRef(initialRoster !== undefined).current;
  // Bumped whenever the feed is replaced whole (a new filter, a jump back to
  // the latest): a read begun before that answers for a list that is gone.
  const generationRef = useRef(0);
  const triedRef = useRef<RunningTries>(new Map());
  const freshTokens = useRef(new Map<string, number>());
  const freshCounter = useRef(0);
  const freshTimers = useRef(new Set<ReturnType<typeof setTimeout>>());
  // A 429's wait, whichever read was refused; every read respects it, and a
  // tab turning visible does not cut it short.
  const quietUntilRef = useRef(0);
  // Consecutive failures, for the back-off; reset by a poll that succeeds.
  const failuresRef = useRef(0);
  // Pages read in a row that said there was more, and the items they held.
  const catchUpRef = useRef({ pages: 0, items: 0 });
  // Every read is made under this, and it is aborted when the screen goes.
  // Filled by the polling effect, which owns its lifetime; null before it runs.
  const abortRef = useRef<AbortController | null>(null);

  /** One read of the feed this screen shows: the contest's, or one participant's timeline. */
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
      // The screen went away mid-read; there is nobody to tell.
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
          // The layout above knows where a lost session goes.
          router.refresh();
          return MONITOR_POLL_MS;
        }
      }
      // A server that is down is not helped by forty tabs asking every five
      // seconds: the wait doubles with each failure in a row, to a ceiling.
      failuresRef.current += 1;
      setProblem({ kind: "failed" });
      return Math.min(MAX_FAILURE_WAIT_MS, MONITOR_POLL_MS * 2 ** (failuresRef.current - 1));
    },
    [router],
  );

  /** Reads the newest page afresh; `gap` counts what is skipped by doing so. */
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
   * One poll; answers the wait before the next, or null to stop.
   *
   * An ordinary poll reads the table and the feed together, lights the rows
   * of whoever has something new, and refreshes running queries. A page that
   * says there is more starts a catch-up: the next ticks read only the feed,
   * a second apart, light nothing (what they bring is old news), and after
   * `MAX_CATCH_UP_PAGES` more pages give the rest up for the newest page.
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
      // A refusal elsewhere (loading older, a new filter) may have asked for
      // quiet since this tick was scheduled.
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
      // Back from a glance at another tab, the last poll is still fresh: the
      // cadence goes on rather than a second read on top of it.
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

  /** Reads the newest page afresh, for a new filter or a jump back to the latest. */
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

  /** Narrows every read to a stretch of time, and reads its newest page afresh. */
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
