"use client";

import { useEffect } from "react";

import type { Game } from "@/lib/api/game";
import { GAME_POLL_MS } from "@/lib/api/game-terms";

import { gameStatusAction } from "./actions";

/**
 * Watching one contest's build, for every panel on the screen that cares.
 *
 * `GameEditor` and `GameUpload` sit on the same page and both need the same
 * fact — has the build finished, and did it fail — so both used to run a
 * `setInterval` of their own over the same endpoint. That is two requests
 * every two seconds for one answer, and worse than the waste: the two ticks
 * are a few milliseconds apart, so on the tick a build completes one panel
 * can be showing "building" while the other already says "ready" or names a
 * failing line.
 *
 * One timer per contest, then, with the panels subscribed to it. They are
 * handed the *same* `Game` object on every tick, which is what makes them
 * unable to disagree; the timer starts when the first panel asks and is
 * cleared when the last one stops, so a page with nothing building costs
 * nothing at all — the reason each panel had its own `building` guard before.
 *
 * Keyed by contest rather than global: the workspace shows one contest at a
 * time today, and a key costs nothing against the day it does not.
 */
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
        // A failed poll is left alone rather than shown: the build is still
        // running as far as anybody knows, and one unreachable request is not
        // news. The next tick asks again.
        if (!fresh) return;
        // Read after the await: a panel may have unsubscribed while the
        // request was in flight, and telling it anything then is a state
        // update on an unmounted component.
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
 * Calls `onGame` with the contest's game every {@link GAME_POLL_MS} while
 * `active`, and stops the moment it is not — a page left polling for ever is
 * a page that costs an idle browser and an idle server something all
 * afternoon.
 *
 * `onGame` has to be stable across renders; a `useState` setter is, which is
 * what both callers pass.
 */
export function useGamePoll(contestId: string, active: boolean, onGame: Watcher): void {
  useEffect(() => {
    if (!active) return;
    return subscribe(contestId, onGame);
  }, [contestId, active, onGame]);
}
