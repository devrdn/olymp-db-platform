"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError } from "@/lib/api/client";
import { MAX_QUERY_SEARCH, type LoggedQuery, type QueriesPage, type QueriesParams, type ReadOptions } from "@/lib/api/journal";

export const SEARCH_DEBOUNCE_MS = 300;

/** Back-off after a refused read when the server named no delay. */
const DEFAULT_QUIET_SECONDS = 60;

export type QueryLogProblem =
  | { kind: "forbidden" }
  | { kind: "tooOften"; seconds: number }
  | { kind: "failed" }
  | null;

export type QueryOutcome = {
  id: number;
  status: string;
  error?: string;
  durationMs: number | null;
  rowCount: number | null;
};

/** Polling for running queries. A finished contest passes none. */
export type QueryLogRefresh = {
  everyMs: number;
  /** Polls per running query before it is left alone. */
  tries: number;
  outcomes(running: LoggedQuery[], options: ReadOptions): Promise<QueryOutcome[]>;
};

export type QueryLogSource = {
  initial: QueriesPage;
  page(params: QueriesParams, options: ReadOptions): Promise<QueriesPage>;
  refresh?: QueryLogRefresh;
};

type Filter = { status: string; q: string };

/**
 * Logged queries, newest first, with filters, keyset paging and polling for
 * running ones.
 *
 * The search is debounced and cut to the length the API accepts, so a paste is
 * not a 400; an answer for a filter that has since changed is dropped. A query
 * is logged as `running` before it runs and keyset paging would not deliver it
 * again, so with `refresh` the hook polls while one is on screen and the tab is
 * visible, at most `refresh.tries` times per query. The caller owns the reads;
 * the two screens use different routes.
 */
export function useQueryLog({ initial, page, refresh }: QueryLogSource) {
  const [items, setItemsState] = useState(initial.items);
  const [more, setMore] = useState(initial.more);
  const [status, setStatusState] = useState("");
  const [search, setSearchState] = useState("");
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [problem, setProblem] = useState<QueryLogProblem>(null);

  const itemsRef = useRef(items);
  const filterRef = useRef<Filter>({ status: "", q: "" });
  // Bumped by every read that replaces the list; an older answer is dropped.
  const generationRef = useRef(0);
  const quietUntilRef = useRef(0);
  const triesRef = useRef(new Map<number, number>());
  const abortRef = useRef<AbortController | null>(null);

  // Held in a ref: inline callers pass a new function every render, which would
  // restart the refresh.
  const pageRef = useRef(page);
  const refreshRef = useRef(refresh);
  // Back-off for a refusal with no delay: the refresh interval, or a minute
  // without one.
  const quietRef = useRef(DEFAULT_QUIET_SECONDS);
  useEffect(() => {
    pageRef.current = page;
    refreshRef.current = refresh;
    quietRef.current = refresh ? refresh.everyMs / 1000 : DEFAULT_QUIET_SECONDS;
  });

  useEffect(() => {
    const controller = new AbortController();
    abortRef.current = controller;
    return () => controller.abort();
  }, []);

  const setItems = useCallback((next: LoggedQuery[]) => {
    itemsRef.current = next;
    setItemsState(next);
  }, []);

  const failed = useCallback((error: unknown) => {
    if (abortRef.current?.signal.aborted) return;
    if (error instanceof ApiError && error.status === 429) {
      const seconds = error.retryAfterSeconds ?? quietRef.current;
      quietUntilRef.current = Date.now() + seconds * 1000;
      setProblem({ kind: "tooOften", seconds });
    } else if (error instanceof ApiError && error.status === 403) {
      setProblem({ kind: "forbidden" });
    } else {
      setProblem({ kind: "failed" });
    }
  }, []);

  const reload = useCallback(async () => {
    const generation = ++generationRef.current;
    setLoading(true);
    try {
      const next = await pageRef.current({ ...filterRef.current }, { signal: abortRef.current?.signal });
      if (generation !== generationRef.current) return;
      triesRef.current.clear();
      setItems(next.items);
      setMore(next.more);
      setProblem(null);
    } catch (error: unknown) {
      if (generation === generationRef.current) failed(error);
    } finally {
      if (generation === generationRef.current) setLoading(false);
    }
  }, [failed, setItems]);

  const searchTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(searchTimerRef.current), []);

  const setStatus = useCallback(
    async (next: string) => {
      setStatusState(next);
      filterRef.current = { ...filterRef.current, status: next };
      await reload();
    },
    [reload],
  );

  const setSearch = useCallback(
    (text: string) => {
      const bounded = text.slice(0, MAX_QUERY_SEARCH);
      setSearchState(bounded);
      clearTimeout(searchTimerRef.current);
      searchTimerRef.current = setTimeout(() => {
        filterRef.current = { ...filterRef.current, q: bounded };
        void reload();
      }, SEARCH_DEBOUNCE_MS);
    },
    [reload],
  );

  const loadMore = useCallback(async () => {
    const last = itemsRef.current.at(-1);
    if (!last) return;
    const generation = generationRef.current;
    setLoadingMore(true);
    try {
      const next = await pageRef.current(
        { ...filterRef.current, cursor: last.cursor },
        { signal: abortRef.current?.signal },
      );
      if (generation !== generationRef.current) return;
      const held = new Set(itemsRef.current.map((q) => q.cursor));
      setItems(itemsRef.current.concat(next.items.filter((q) => !held.has(q.cursor))));
      setMore(next.more);
      setProblem(null);
    } catch (error: unknown) {
      failed(error);
    } finally {
      setLoadingMore(false);
    }
  }, [failed, setItems]);

  // Alive while something is running, paused while the tab is hidden. Zero
  // means no refresh, so the effect depends on numbers rather than an object
  // rebuilt every render.
  const everyMs = refresh?.everyMs ?? 0;
  const maxTries = refresh?.tries ?? 0;
  useEffect(() => {
    if (everyMs === 0 || maxTries === 0) return;
    const tries = triesRef.current;
    const live = () => itemsRef.current.filter((q) => q.status === "running" && (tries.get(q.id) ?? 0) < maxTries);
    if (live().length === 0) return;

    let cancelled = false;
    let inFlight = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const controller = new AbortController();
    const visible = () => document.visibilityState !== "hidden";
    const schedule = (ms: number) => {
      clearTimeout(timer);
      timer = setTimeout(tick, ms);
    };

    async function tick() {
      timer = undefined;
      if (cancelled || !visible()) return;
      const quiet = quietUntilRef.current - Date.now();
      if (quiet > 0) {
        schedule(quiet);
        return;
      }
      const running = live();
      if (running.length === 0) return;
      for (const q of running) tries.set(q.id, (tries.get(q.id) ?? 0) + 1);
      inFlight = true;
      try {
        const answered = await refreshRef.current?.outcomes(running, { signal: controller.signal });
        if (cancelled) return;
        const outcome = new Map<number, LoggedQuery>();
        for (const ended of answered ?? []) {
          const held = itemsRef.current.find((q) => q.id === ended.id);
          if (
            held &&
            (held.status !== ended.status || held.durationMs !== ended.durationMs || held.rowCount !== ended.rowCount)
          ) {
            outcome.set(ended.id, {
              ...held,
              status: ended.status,
              error: ended.error,
              durationMs: ended.durationMs,
              rowCount: ended.rowCount,
            });
          }
        }
        if (outcome.size > 0) setItems(itemsRef.current.map((q) => outcome.get(q.id) ?? q));
        setProblem(null);
      } catch (error: unknown) {
        if (!cancelled) failed(error);
      } finally {
        inFlight = false;
      }
      if (!cancelled && visible()) schedule(everyMs);
    }

    const onVisibility = () => {
      if (!visible()) {
        clearTimeout(timer);
        timer = undefined;
      } else if (!inFlight && timer === undefined) {
        schedule(0);
      }
    };

    document.addEventListener("visibilitychange", onVisibility);
    schedule(everyMs);
    return () => {
      cancelled = true;
      controller.abort();
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [everyMs, maxTries, items, failed, setItems]);

  return { items, more, status, setStatus, search, setSearch, loading, loadMore, loadingMore, problem };
}

export type QueryLogState = ReturnType<typeof useQueryLog>;
