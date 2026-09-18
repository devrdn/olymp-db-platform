"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError } from "@/lib/api/client";
import {
  fetchQueries,
  fetchTimeline,
  MAX_FEED_PAGE,
  MAX_QUERY_SEARCH,
  type LoggedQuery,
  type QueriesPage,
} from "@/lib/api/monitor";

import { MAX_RUNNING_TRIES } from "../feed-list";
import { MONITOR_POLL_MS, type MonitorProblem } from "../use-monitor";

/** How long the search waits for the typing to stop. */
export const SEARCH_DEBOUNCE_MS = 300;

type Filter = { status: string; q: string };

/**
 * The queries tab's list: one participant's queries, newest first, whole.
 *
 * - **Filters.** A status reads the first page afresh at once; the search
 *   waits for the typing to stop (`SEARCH_DEBOUNCE_MS`) and is cut to the
 *   length the API takes, so a pasted essay is one bounded question, not a
 *   400. An answer to a filter that has since changed is dropped.
 * - **More.** Keyset: the next page is asked after the last query held.
 * - **Running queries.** A query is journalled as `running` before it runs,
 *   and the queries route is keyset by position, so it would not deliver it
 *   again. While one is on screen and the tab is visible, the timeline is
 *   asked for that stretch every five seconds (`MONITOR_POLL_MS`), and the
 *   outcome — status, error, duration, rows — is put in place; the whole
 *   statement already held stays. Each query is asked about at most
 *   `MAX_RUNNING_TRIES` times, as on the contest's feed.
 */
export function useQueries({
  contestId,
  registrationId,
  initial,
}: {
  contestId: string;
  registrationId: string;
  initial: QueriesPage;
}) {
  const [items, setItemsState] = useState(initial.items);
  const [more, setMore] = useState(initial.more);
  const [status, setStatusState] = useState("");
  const [search, setSearchState] = useState("");
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [problem, setProblem] = useState<MonitorProblem>(null);

  const itemsRef = useRef(items);
  const filterRef = useRef<Filter>({ status: "", q: "" });
  // Bumped by every read that replaces the list; an older answer is dropped.
  const generationRef = useRef(0);
  const quietUntilRef = useRef(0);
  const triesRef = useRef(new Map<number, number>());
  const abortRef = useRef<AbortController | null>(null);

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
      const seconds = error.retryAfterSeconds ?? MONITOR_POLL_MS / 1000;
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
      const page = await fetchQueries(
        contestId,
        registrationId,
        { ...filterRef.current },
        { signal: abortRef.current?.signal },
      );
      if (generation !== generationRef.current) return;
      triesRef.current.clear();
      setItems(page.items);
      setMore(page.more);
      setProblem(null);
    } catch (error: unknown) {
      if (generation === generationRef.current) failed(error);
    } finally {
      if (generation === generationRef.current) setLoading(false);
    }
  }, [contestId, registrationId, failed, setItems]);

  // The search's pending read: one at a time, the latest text wins.
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
      const page = await fetchQueries(
        contestId,
        registrationId,
        { ...filterRef.current, cursor: last.cursor },
        { signal: abortRef.current?.signal },
      );
      if (generation !== generationRef.current) return;
      const held = new Set(itemsRef.current.map((q) => q.cursor));
      setItems(itemsRef.current.concat(page.items.filter((q) => !held.has(q.cursor))));
      setMore(page.more);
      setProblem(null);
    } catch (error: unknown) {
      failed(error);
    } finally {
      setLoadingMore(false);
    }
  }, [contestId, registrationId, failed, setItems]);

  // The refresh of running queries: a chain of its own, alive while there is
  // something running to ask about, quiet while the tab is hidden.
  useEffect(() => {
    const tries = triesRef.current;
    const live = () =>
      itemsRef.current.filter((q) => q.status === "running" && (tries.get(q.id) ?? 0) < MAX_RUNNING_TRIES);
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
      let from = running[0].executedAt;
      let until = from;
      for (const q of running) {
        tries.set(q.id, (tries.get(q.id) ?? 0) + 1);
        if (q.executedAt < from) from = q.executedAt;
        if (q.executedAt > until) until = q.executedAt;
      }
      inFlight = true;
      try {
        const page = await fetchTimeline(
          contestId,
          registrationId,
          // `from` inclusive, `until` exclusive, and times to the millisecond.
          { kinds: ["query"], from, until: new Date(Date.parse(until) + 1).toISOString(), limit: MAX_FEED_PAGE },
          { signal: controller.signal },
        );
        if (cancelled) return;
        const outcome = new Map<number, LoggedQuery>();
        for (const item of page.items) {
          const d = item.detail;
          if (d.type !== "query") continue;
          const held = itemsRef.current.find((q) => q.id === d.id);
          if (held && (held.status !== d.status || held.durationMs !== d.durationMs || held.rowCount !== d.rowCount)) {
            outcome.set(d.id, { ...held, status: d.status, error: d.error, durationMs: d.durationMs, rowCount: d.rowCount });
          }
        }
        if (outcome.size > 0) setItems(itemsRef.current.map((q) => outcome.get(q.id) ?? q));
      } catch (error: unknown) {
        if (!cancelled) failed(error);
      } finally {
        inFlight = false;
      }
      if (!cancelled && visible()) schedule(MONITOR_POLL_MS);
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
    schedule(MONITOR_POLL_MS);
    return () => {
      cancelled = true;
      controller.abort();
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [contestId, registrationId, items, failed, setItems]);

  return { items, more, status, setStatus, search, setSearch, loading, loadMore, loadingMore, problem };
}
