"use client";

/**
 * One shared build poll per contest. Every subscribed panel gets the same
 * `Game` object each tick, so panels cannot disagree on the tick a build ends;
 * separate timers did, and doubled the requests. The timer runs only while some
 * panel is subscribed.
 */

import { useEffect } from "react";

import type { Game } from "@/lib/api/game";
import { GAME_POLL_MS } from "@/lib/api/game-terms";

import { gameStatusAction } from "./actions";

type Watcher = (game: Game) => void;

const watchers = new Map<string, Set<Watcher>>();
const timers = new Map<string, ReturnType<typeof setInterval>>();

function subscribe(contestId: string, watcher: Watcher): () => void {
  let group = watchers.get(contestId);
  if (!group) {
    group = new Set();
    watchers.set(contestId, group);
  }
  group.add(watcher);

  if (!timers.has(contestId)) {
    timers.set(
      contestId,
      setInterval(async () => {
        const fresh = await gameStatusAction(contestId);
        // A failed poll is not news; the next tick asks again.
        if (!fresh) return;
        // Read after the await: a panel may have unsubscribed meanwhile.
        for (const notify of watchers.get(contestId) ?? []) notify(fresh);
      }, GAME_POLL_MS),
    );
  }

  return () => {
    group.delete(watcher);
    if (group.size > 0) return;
    clearInterval(timers.get(contestId));
    timers.delete(contestId);
    watchers.delete(contestId);
  };
}

/**
 * Calls `onGame` every {@link GAME_POLL_MS} while `active`, and stops when it
 * is not. `onGame` must be stable across renders (a `useState` setter is).
 */
export function useGamePoll(contestId: string, active: boolean, onGame: Watcher): void {
  useEffect(() => {
    if (!active) return;
    return subscribe(contestId, onGame);
  }, [contestId, active, onGame]);
}
