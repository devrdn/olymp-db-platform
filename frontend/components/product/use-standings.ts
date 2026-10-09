"use client";

import { useEffect, useRef, useState } from "react";

import type { Standings, StandingsState } from "@/lib/api/leaderboard";

export type StandingsResult = { kind: "ok"; standings: Standings } | { kind: "refused"; code: string };

/**
 * Poll interval per state in ms, or null for never. Frozen and not-started
 * tables are polled only to notice the reveal or start. The server computes a
 * table at most once per ten seconds per contest regardless.
 */
export const STANDINGS_POLL_MS: Record<StandingsState, number | null> = {
  live: 15_000,
  frozen: 60_000,
  not_started: 60_000,
  final: null,
};

/**
 * Polls while the table is on screen (`active`) and the tab is visible. A
 * failed refresh keeps the last copy and says so.
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
  // Bumped after every read, so the next one is scheduled even when the copy is
  // identical.
  const [reads, setReads] = useState(0);
  // Read through a ref so a fresh closure per render does not restart polling.
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

    // Nothing yet: ask now. An unreadable table is retried like a live one.
    const wait = state === undefined ? (reads === 0 ? 0 : STANDINGS_POLL_MS.live) : STANDINGS_POLL_MS[state];
    if (wait !== null) timer = setTimeout(read, wait);

    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
  }, [active, visible, state, reads]);

  return { standings, failed };
}
