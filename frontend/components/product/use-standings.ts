"use client";

import { useEffect, useRef, useState } from "react";

import type { Standings, StandingsState } from "@/lib/api/leaderboard";

/** What one read of the table turned up. */
export type StandingsResult = { kind: "ok"; standings: Standings } | { kind: "refused"; code: string };

/**
 * How often each state is asked for again, in milliseconds, or null for never.
 *
 * A live table every fifteen seconds; a frozen one and one that has not
 * started once a minute, only to notice the reveal or the start; a final one
 * never, because nothing about it will change. The server computes a table at
 * most once per ten seconds per contest whatever this says.
 */
export const STANDINGS_POLL_MS: Record<StandingsState, number | null> = {
  live: 15_000,
  frozen: 60_000,
  not_started: 60_000,
  final: null,
};

/**
 * Keeps a copy of the table fresh while somebody is looking at it.
 *
 * `active` is whether the table is on screen at all — the play tab is mounted
 * the whole time but read only when it is selected — and a hidden browser tab
 * counts as not looking. A failed refresh keeps the last copy and says so,
 * rather than blanking a table somebody was reading.
 */
export function useStandings({
  load,
  active,
  initial,
}: {
  load: () => Promise<StandingsResult>;
  active: boolean;
  initial?: Standings;
}) {
  const [standings, setStandings] = useState<Standings | undefined>(initial);
  const [failed, setFailed] = useState(false);
  const [visible, setVisible] = useState(true);
  // Moves after every read, answered or refused, so the next one is always
  // scheduled — even when the server's cache hands back an identical copy.
  const [reads, setReads] = useState(0);
  // The latest loader, updated after render: a caller that passes a fresh
  // closure each render must not restart the polling interval.
  const loadRef = useRef(load);
  useEffect(() => {
    loadRef.current = load;
  }, [load]);

  useEffect(() => {
    const update = () => setVisible(!document.hidden);
    update();
    document.addEventListener("visibilitychange", update);
    return () => document.removeEventListener("visibilitychange", update);
  }, []);

  const state = standings?.state;

  useEffect(() => {
    if (!active || !visible) return;

    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;

    const read = async () => {
      const result = await loadRef.current();
      if (cancelled) return;
      if (result.kind === "ok") {
        setStandings(result.standings);
        setFailed(false);
      } else {
        setFailed(true);
      }
      setReads((n) => n + 1);
    };

    // Nothing yet: ask now. A copy in hand: ask after its state's interval —
    // and a table that could not be read at all is retried like a live one.
    const wait = state === undefined ? (reads === 0 ? 0 : STANDINGS_POLL_MS.live) : STANDINGS_POLL_MS[state];
    if (wait !== null) timer = setTimeout(read, wait);

    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
  }, [active, visible, state, reads]);

  return { standings, failed };
}
