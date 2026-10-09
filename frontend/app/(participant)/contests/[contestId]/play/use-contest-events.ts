"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import { API_PREFIX } from "@/lib/api/client";

import { isClosed, refusalKind } from "./refusals";

export type ContestPhase = "waiting" | "running" | "finished";

type SyncPayload = { server_now: string; deadline?: string };

/**
 * Whether a refusal of the channel cannot clear on a timer: the participant
 * is excluded (disqualified, say) or is outside the contest's network. The
 * header says why meanwhile.
 */
function isTerminal(code: string): boolean {
  const kind = refusalKind(code);
  return kind === "excluded" || kind === "elsewhere";
}

/** The first reconnect delay, and the ceiling the doubling backoff stops at. */
const RECONNECT_MIN_DELAY_MS = 5_000;
const RECONNECT_MAX_DELAY_MS = 60_000;

/**
 * Random spread added to a reconnect delay: a wait falls in
 * `[delay, delay * 1.5)`. Added, never subtracted, because every attempt
 * spends the query-rate budget this channel shares with the SQL console. A
 * deploy cuts every channel at once, and without jitter they would all return
 * in the same instant against the per-participant connection cap.
 */
const RECONNECT_JITTER = 0.5;

/**
 * The one Server-Sent Events connection this screen opens, and the one place
 * the browser clock is corrected against the server's. Its consumers (the
 * waiting room and the running header) are never on screen together, so
 * there is never more than one connection.
 *
 * `offsetRef` and `deadlineRef` are refs: callers tick once a second and read
 * `.current`, so a resync every thirty seconds need not render. `phase` is
 * state: it moves at most twice a contest, each time with something new to
 * show. `initialPhase` is what the page already knows, so the screen does not
 * flash "not started" while the connection opens. The connection depends
 * only on `contestId` and closes on unmount, freeing the connection slot.
 *
 * While `EventSource` is still `CONNECTING` it retries on the server's
 * `retry:` interval and this hook does nothing. A non-200 response fails the
 * connection permanently (`CLOSED`), which would freeze the clock silently;
 * then a plain `fetch` of the same URL asks why. A refusal that may clear (a
 * rate limit, the connection cap, a server error, `contest_not_running`) is
 * shown as `channelError` and retried; a terminal one (see isTerminal) is
 * shown and not retried; an admitted probe is retried with no message. Every
 * retry goes through `scheduleReconnect`.
 *
 * `renderedDormant` is the caller's word that the page was rendered from a
 * `dormant` refusal (content-loaded.tsx); the channel cannot learn that
 * itself when the contest opened before its first connection.
 */
export function useContestEvents(
  contestId: string,
  initialPhase: ContestPhase = "waiting",
  renderedDormant = false,
) {
  const offsetRef = useRef(0);
  // `undefined`: no sync yet. `null`: the server said this participant has no
  // deadline, which happens only under individual timing before their first
  // action. Conflating them would show "Starts with your first action" in a
  // fixed-window contest before the channel connects.
  const deadlineRef = useRef<number | null | undefined>(undefined);
  const [phase, setPhase] = useState<ContestPhase>(initialPhase);
  const [channelError, setChannelError] = useState<string | null>(null);
  // Whether a connection was admitted after a `dormant` refusal, or after
  // the page was rendered from one (`renderedDormant`). The contest opening
  // is not pushed; it is only the next connection being admitted, and the
  // page rendered from that refusal is stale from then. Stays true once set.
  const [reopened, setReopened] = useState(false);
  // Read by the channel, which outlives every render.
  const renderedDormantRef = useRef(renderedDormant);
  // Whether any connection has been admitted. A ref: an ordinary sync must
  // not render.
  const admittedRef = useRef(false);
  // The live channel's resync; a no-op before it opens and after unmount.
  const resyncRef = useRef<() => void>(() => {});

  // The page streams in later, so it can report rendering dormant after the
  // channel was already admitted: that is the reopening too.
  useEffect(() => {
    renderedDormantRef.current = renderedDormant;
    if (renderedDormant && admittedRef.current) setReopened(true);
  }, [renderedDormant]);

  useEffect(() => {
    const url = `${API_PREFIX}/contests/${contestId}/events`;
    admittedRef.current = false;
    let cancelled = false;
    let source: EventSource | null = null;
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    let retryDelay = RECONNECT_MIN_DELAY_MS;
    // Set by a `dormant` refusal and cleared by the next sync, which reports
    // `reopened`. Other refusals in between leave it set.
    let dormant = false;

    const clearRetryTimer = () => {
      if (retryTimer !== null) {
        clearTimeout(retryTimer);
        retryTimer = null;
      }
    };

    /**
     * Waits, then opens a fresh connection: the only path back onto the
     * channel, whatever failed. The delay doubles up to the ceiling for every
     * cause, including an admitted probe; a fixed floor there would reconnect
     * every five seconds for the whole contest, each turn spending two charges
     * of the budget shared with the SQL console (AdmitRead). A `sync` resets
     * it, since it arrives only on an accepted connection.
     */
    const scheduleReconnect = () => {
      const delay = retryDelay * (1 + Math.random() * RECONNECT_JITTER);
      retryDelay = Math.min(retryDelay * 2, RECONNECT_MAX_DELAY_MS);
      clearRetryTimer();
      retryTimer = setTimeout(() => {
        if (cancelled) return;
        connect();
      }, delay);
    };

    const connect = () => {
      const es = new EventSource(url);
      source = es;

      const onSync = (event: MessageEvent) => {
        try {
          const data = JSON.parse(event.data) as SyncPayload;
          offsetRef.current = new Date(data.server_now).getTime() - Date.now();
          deadlineRef.current = data.deadline ? new Date(data.deadline).getTime() : null;
          // A sync arrives only on an accepted connection, so any earlier banner
          // and backoff are stale.
          retryDelay = RECONNECT_MIN_DELAY_MS;
          setChannelError(null);
          admittedRef.current = true;
          if (dormant || renderedDormantRef.current) {
            dormant = false;
            setReopened(true);
          }
        } catch {
          // A malformed push changes nothing; the next sync corrects it.
        }
      };
      const onStarted = () => setPhase("running");
      const onFinished = () => setPhase("finished");

      const onError = () => {
        if (cancelled) return;
        if (es.readyState !== EventSource.CLOSED) {
          // CONNECTING: the browser is retrying by itself.
          return;
        }
        void diagnose(url).then((result) => {
          if (cancelled) return;
          if (result.ok) {
            // The probe was admitted, so the failure was a one-off (a proxy, say).
            // No banner, but the same backoff as any other reconnect.
            setChannelError(null);
            scheduleReconnect();
            return;
          }
          const code = result.code ?? "unreachable";
          if (isClosed(code)) {
            // The contest is over for this participant. Reached here only when
            // the refusal comes at connect time, e.g. a page opened after the
            // participant's deadline.
            setPhase("finished");
            return;
          }
          // A contest not open now (`dormant`: a published contest taken back
          // to draft, or an individual window not yet open) is neither over nor
          // terminal: it is shown, retried until a sync clears it, and then
          // reported as `reopened`.
          if (refusalKind(code) === "dormant") dormant = true;
          setChannelError(code);
          if (isTerminal(code)) {
            // A timer would only repeat the same refusal.
            return;
          }
          scheduleReconnect();
        });
      };

      es.addEventListener("sync", onSync);
      es.addEventListener("contest_started", onStarted);
      es.addEventListener("contest_finished", onFinished);
      es.addEventListener("error", onError);
    };

    connect();

    /**
     * Asks for one fresh sync now, at most once per channel. The server syncs
     * only when a connection opens, so this reopens the channel. Bounded to
     * one because every connection spends the read budget shared with the SQL
     * console. A failed connection is left to its scheduled reconnect, whose
     * first sync is the fresh one.
     */
    let resynced = false;
    resyncRef.current = () => {
      if (cancelled || resynced) return;
      resynced = true;
      if (source === null || source.readyState === EventSource.CLOSED) return;
      source.close();
      connect();
    };

    return () => {
      cancelled = true;
      clearRetryTimer();
      source?.close();
      resyncRef.current = () => {};
    };
  }, [contestId]);

  // Stable, so a caller can list it as an effect dependency.
  const resync = useCallback(() => resyncRef.current(), []);

  return { offsetRef, deadlineRef, phase, channelError, resync, reopened };
}

/** An admission, or the API's reason for refusing one. */
type DiagnoseResult = { ok: true } | { ok: false; code: string | null };

/**
 * Asks with a plain request why the channel was refused: an `EventSource`
 * error carries no status or body. `code` is null when the body is not the
 * API's error envelope (a gateway's HTML page, say). An admitted probe's
 * stream is cancelled at once so it does not hold one of the participant's
 * connection slots.
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
