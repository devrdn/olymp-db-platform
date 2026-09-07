"use client";

import { useEffect, useRef, useState } from "react";

import { API_PREFIX } from "@/lib/api/client";

export type ContestPhase = "waiting" | "running" | "finished";

type SyncPayload = { server_now: string; deadline?: string };

/**
 * The one Server-Sent Events connection this screen ever opens, and the one
 * place a participant's browser clock is corrected against the server's.
 *
 * A single hook rather than one per consumer, because the screens that read
 * from it — the waiting room and the running header's clock — are never on
 * screen at the same time: which one renders is decided by `phase`, so there
 * is never more than one connection open regardless of how many places call
 * this.
 *
 * `offsetRef` and `deadlineRef` are refs, not state, on purpose: a caller
 * that ticks once a second reads `.current` at render time, and a value that
 * changed every thirty seconds (the server's own resync interval) has no
 * business re-rendering a component that only reads it once a second anyway —
 * the caller's own interval already guarantees the read is never stale by
 * more than a second. `phase` is state, because it is the one thing here that
 * should cause a render when it changes: it moves at most twice in two hours
 * (contest_started, contest_finished), and each time is exactly the moment
 * the screen has something new to say.
 *
 * The connection survives every re-render of whoever calls this — the effect
 * below depends on nothing but `contestId` — and is closed the moment the
 * caller unmounts, which is what keeps a participant who navigates away from
 * leaving a socket, and this installation's connection limit, behind them.
 *
 * `initialPhase` seeds the state the server already knows before the first
 * event ever arrives — the page that rendered this screen already asked the
 * API whether the contest is running, and starting from "waiting" regardless
 * would flash a "not started" clock for the instant it takes the connection
 * to open and say what this page was already told.
 *
 * Reconnection on a dropped connection is left entirely to the browser's own
 * EventSource: the server sends its own `retry:` interval once, at connect
 * time (events_handler.go's own doc, finding 3), and a client that reconnects
 * at all does so on that schedule. Nothing here ever calls `.close()` and
 * opens a fresh EventSource to "retry sooner" — that is exactly the loop this
 * screen must not become.
 */
export function useContestEvents(contestId: string, initialPhase: ContestPhase = "waiting") {
  const offsetRef = useRef(0);
  const deadlineRef = useRef<number | null>(null);
  const [phase, setPhase] = useState<ContestPhase>(initialPhase);

  useEffect(() => {
    const source = new EventSource(`${API_PREFIX}/contests/${contestId}/events`);

    const onSync = (event: MessageEvent) => {
      try {
        const data = JSON.parse(event.data) as SyncPayload;
        offsetRef.current = new Date(data.server_now).getTime() - Date.now();
        deadlineRef.current = data.deadline ? new Date(data.deadline).getTime() : null;
      } catch {
        // A malformed push changes nothing; the next sync, at most thirty
        // seconds later, corrects it.
      }
    };
    const onStarted = () => setPhase("running");
    const onFinished = () => setPhase("finished");

    source.addEventListener("sync", onSync);
    source.addEventListener("contest_started", onStarted);
    source.addEventListener("contest_finished", onFinished);

    return () => {
      source.removeEventListener("sync", onSync);
      source.removeEventListener("contest_started", onStarted);
      source.removeEventListener("contest_finished", onFinished);
      source.close();
    };
  }, [contestId]);

  return { offsetRef, deadlineRef, phase };
}
