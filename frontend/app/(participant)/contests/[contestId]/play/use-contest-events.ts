"use client";

import { useEffect, useRef, useState } from "react";

import { API_PREFIX } from "@/lib/api/client";

export type ContestPhase = "waiting" | "running" | "finished";

type SyncPayload = { server_now: string; deadline?: string };

/**
 * Error codes a closed channel is refused with that reconnecting cannot fix:
 * this participant's own access to the contest was revoked (disqualified, or
 * the address they are on stopped being allowed), not something that clears
 * on its own the way a rate limit or a briefly busy server does.
 */
const TERMINAL_CODES = new Set(["not_a_participant", "address_not_allowed"]);

/**
 * Error codes that mean the contest is over for this participant — the same
 * fact the resync loop's own ErrFinished/ErrContestNotRunning branch already
 * announces as `contest_finished` once a connection is open
 * (events_handler.go's own doc). Reached here only when that same refusal
 * happens at connect time instead — a page opened after the participant's
 * own deadline already passed, say.
 */
const FINISHED_CODES = new Set(["contest_finished", "contest_not_running"]);

/** How long to wait before the first reconnect attempt, and the ceiling a doubling backoff is capped at. */
const RECONNECT_MIN_DELAY_MS = 5_000;
const RECONNECT_MAX_DELAY_MS = 60_000;

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
 * Reconnection on a connection the browser is still trying by itself is left
 * entirely to `EventSource`: the server sends its own `retry:` interval once,
 * at connect time (events_handler.go's own doc, finding 3), and a client that
 * reconnects at all does so on that schedule — this hook never calls
 * `.close()` on a live connection to "retry sooner".
 *
 * A connection the browser gives up on outright is a different case, and the
 * one this hook does handle. Per the SSE specification, a non-200 response —
 * a rate limit, this installation's own cap on how many of this channel one
 * participant may hold, a refusal because this account no longer belongs
 * here — fails the connection permanently: `readyState` becomes `CLOSED` and
 * the browser never tries again on its own. Left alone, the clock this
 * connection drives freezes on whatever it last showed for the rest of the
 * contest, silently. The `error` listener below is what tells the two cases
 * apart (`readyState` still `CONNECTING` means the browser itself is
 * retrying — nothing to do) and, only for a `CLOSED` connection, asks a plain
 * `fetch` of the same URL why: a code this installation expects to clear on
 * its own (too many connections, a rate limit, a transient server error)
 * gets a capped, doubling-backoff reconnect and a translated reason exposed
 * as `channelError` for the screen to show meanwhile; a code that will never
 * clear on its own (this account was removed from the contest, or the
 * address it is on stopped being allowed) gets the same message with no
 * retry, since nothing this tab does will change either fact.
 */
export function useContestEvents(contestId: string, initialPhase: ContestPhase = "waiting") {
  const offsetRef = useRef(0);
  // Three states, not two. `undefined` is "no sync has arrived yet"; `null`
  // is "a sync arrived and the server said this participant has no deadline",
  // which happens only under individual timing before their first action.
  // Conflating them is what put "Starts with your first action" on the screen
  // of a fixed-window contest for the moment before the channel connected —
  // a sentence that is not merely early there, it is false.
  const deadlineRef = useRef<number | null | undefined>(undefined);
  const [phase, setPhase] = useState<ContestPhase>(initialPhase);
  const [channelError, setChannelError] = useState<string | null>(null);

  useEffect(() => {
    const url = `${API_PREFIX}/contests/${contestId}/events`;
    let cancelled = false;
    let source: EventSource | null = null;
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    let retryDelay = RECONNECT_MIN_DELAY_MS;

    const clearRetryTimer = () => {
      if (retryTimer !== null) {
        clearTimeout(retryTimer);
        retryTimer = null;
      }
    };

    const connect = () => {
      const es = new EventSource(url);
      source = es;

      const onSync = (event: MessageEvent) => {
        try {
          const data = JSON.parse(event.data) as SyncPayload;
          offsetRef.current = new Date(data.server_now).getTime() - Date.now();
          deadlineRef.current = data.deadline ? new Date(data.deadline).getTime() : null;
          // A sync only ever arrives on a connection the server just
          // accepted: whatever caused an earlier failure is over, so any
          // banner and backoff from it are stale.
          retryDelay = RECONNECT_MIN_DELAY_MS;
          setChannelError(null);
        } catch {
          // A malformed push changes nothing; the next sync, at most thirty
          // seconds later, corrects it.
        }
      };
      const onStarted = () => setPhase("running");
      const onFinished = () => setPhase("finished");

      const onError = () => {
        if (cancelled) return;
        if (es.readyState !== EventSource.CLOSED) {
          // The browser itself is retrying this exact connection (readyState
          // CONNECTING) — a transient drop, and exactly the case left to
          // EventSource's own reconnection (see this hook's own doc).
          return;
        }
        void diagnose(url).then((result) => {
          if (cancelled) return;
          if (result.ok) {
            // The probe itself was admitted: the server would take a fresh
            // connection right now, so whatever failed the first one was a
            // one-off (a proxy hiccup, say) rather than a standing refusal.
            // No banner and no backoff growth — this was never the kind of
            // failure the backoff exists for — but still a floor, not an
            // immediate reconnect: if EventSource keeps failing on this URL
            // while a plain fetch of it keeps succeeding (a proxy that
            // handles the two differently, say), reconnecting with no delay
            // at all would spin as fast as the network allows, and every
            // turn spends the query-rate budget this channel shares with the
            // SQL console (config.QueryPerMinute's own doc, AdmitRead).
            setChannelError(null);
            retryDelay = RECONNECT_MIN_DELAY_MS;
            clearRetryTimer();
            retryTimer = setTimeout(() => {
              if (cancelled) return;
              connect();
            }, RECONNECT_MIN_DELAY_MS);
            return;
          }
          const code = result.code ?? "unreachable";
          if (FINISHED_CODES.has(code)) {
            // Not an error to show — the contest is simply over for this
            // participant, the same fact the resync loop announces as
            // contest_finished when it happens after a connection is
            // already open.
            setPhase("finished");
            return;
          }
          setChannelError(code);
          if (TERMINAL_CODES.has(code)) {
            // Reconnecting cannot change who this account is or where it is
            // connecting from; retrying would only repeat the same refusal.
            return;
          }
          clearRetryTimer();
          retryTimer = setTimeout(() => {
            if (cancelled) return;
            retryDelay = Math.min(retryDelay * 2, RECONNECT_MAX_DELAY_MS);
            connect();
          }, retryDelay);
        });
      };

      es.addEventListener("sync", onSync);
      es.addEventListener("contest_started", onStarted);
      es.addEventListener("contest_finished", onFinished);
      es.addEventListener("error", onError);
    };

    connect();

    return () => {
      cancelled = true;
      clearRetryTimer();
      source?.close();
    };
  }, [contestId]);

  return { offsetRef, deadlineRef, phase, channelError };
}

/** What asking the same URL again turned up: an admission, or the API's own reason for refusing one. */
type DiagnoseResult = { ok: true } | { ok: false; code: string | null };

/**
 * Asks, with a plain request against the same URL, why the channel's own
 * connection attempt was refused outright — `EventSource`'s `error` event
 * carries no status code or body, only the fact that something went wrong
 * (this hook's own doc). `code` is null when the response failed but carried
 * nothing this side could parse as the API's own error envelope (a gateway's
 * HTML page, say) — the same "unreachable" a client-side ApiError would
 * synthesise for that shape (lib/api/client.ts's own toApiError).
 *
 * A successful probe's own stream is cancelled at once: this call exists
 * only to read the response, never to hold a second connection open, and
 * leaving it running would burn one of this participant's own connLimiter
 * slots for nothing (events_handler.go's own doc on that limit).
 */
async function diagnose(url: string): Promise<DiagnoseResult> {
  try {
    const response = await fetch(url);
    if (response.ok) {
      void response.body?.cancel().catch(() => {});
      return { ok: true };
    }
    const raw = await response.text();
    try {
      const body = JSON.parse(raw) as { error?: { code?: string } };
      return { ok: false, code: body?.error?.code ?? null };
    } catch {
      return { ok: false, code: null };
    }
  } catch {
    return { ok: false, code: null };
  }
}
