"use client";

import { useEffect } from "react";

import { ApiError } from "@/lib/api/client";
import { sendSignals, type PasteTarget, type Signal } from "@/lib/api/workspace";

import { refusalKind } from "./refusals";

export type { PasteTarget, Signal };

/**
 * What the play screen tells the organiser about its own participant's
 * browser (docs/ARCHITECTURE.md §9.4,
 * §2.2): leaving the page, and pasting into the SQL editor, an answer or the
 * notes. The organiser's screen calls these signals, not proof — the browser
 * reports them, and a participant's browser can be made to say anything —
 * and the participant is told on this same screen that they are observed
 * (§8).
 *
 * The rules, all kept by `SignalCollector` below:
 *
 * - an absence starts when the page is hidden (`visibilitychange`) or the
 *   window loses focus (`blur`), whichever comes first, and ends on the first
 *   return (visible again, or focused again); a hide and a blur that overlap
 *   are one absence. It is recorded on the return, and only when it lasted a
 *   second or more — or, when the page goes away (`pagehide`) without the
 *   participant coming back, then, with the time away so far;
 * - a paste is recorded where it lands inside an element marked with
 *   `data-paste-target` (`editor`, `answer` or `notes`), with its length and
 *   its first 500 characters. One listener on the document, in the capture
 *   phase and passive, so it sees the paste before CodeMirror or React do and
 *   never changes what the paste does;
 * - signals wait in memory and leave in batches of at most 50: every 10 s
 *   when there is something to send, and at once — as a `keepalive` request —
 *   when the page is hidden or goes away (`pagehide`). A hide less than 5 s
 *   after the last batch left sends nothing of its own: switching tabs
 *   quickly would otherwise spend the twelve batches a minute on hides and
 *   silence the organiser's live view for a minute; the timer and
 *   `pagehide` still send;
 * - a batch lost on the network, refused for the rate (429, after its
 *   `Retry-After`) or by a server error goes back into the buffer, in the
 *   order the signals happened, and leaves again later. The buffer holds at most 200 signals; past that
 *   the oldest go first, so an unreachable server never costs more memory
 *   than that;
 * - a batch refused as such (another 4xx) is dropped: sending it again would
 *   be refused again;
 * - once the contest has closed for the participant (409
 *   `contest_ended`, `contest_finished` or `deadline_passed`), the
 *   participant is not on its roster (403 `not_a_participant`), or the
 *   session has ended (any 401), the collector stops for good:
 *   every later batch would be refused alike. `address_not_allowed` is not
 *   final — a laptop briefly on a hotspot is outside the network for a
 *   moment — so that batch is dropped like any other refusal and collecting
 *   goes on, at one refused request per 10 s at most;
 * - `contest_not_running` (a contest not open now) does not stop it either,
 *   and needs nothing of its own: signals are collected only on the running
 *   screen, and a running contest's one way onward is to finish — it can
 *   never go back to draft — so it cannot arrive mid-work. Should it arrive
 *   anyway, the batch is dropped like any other refusal;
 * - a page restored from the back/forward cache (`pageshow` with
 *   `persisted`) records the time since its `pagehide` as an absence.
 *
 * Nothing here is React state: a signal never re-renders the screen.
 */

/** How often a non-empty buffer is sent. */
export const SIGNAL_FLUSH_MS = 10_000;
/** The shortest absence recorded (`monitor.MinAway`). */
export const SIGNAL_MIN_AWAY_MS = 1000;
/** The most signals one batch carries (`monitor.MaxBatchEvents`). */
export const SIGNAL_BATCH_MAX = 50;
/** The most signals kept waiting; past it the oldest are dropped. */
export const SIGNAL_BUFFER_MAX = 200;
/** How much of a paste is kept (`monitor.MaxPasteTextRunes`). */
export const SIGNAL_PASTE_TEXT_MAX = 500;
/**
 * The largest body a `keepalive` batch may have. Browsers allow 64 KiB of
 * `keepalive` bodies in flight per page, shared with the autosave's own last
 * save on the way out, which matters more. Keeping the signals to a quarter
 * of it is a best-effort share, not a reserve: whichever `pagehide` listener
 * runs first takes the quota first, and nothing here decides that order.
 * Whatever does not fit waits for the next send.
 */
export const SIGNAL_KEEPALIVE_BYTES = 16 * 1024;
/** A hide this soon after the last batch left does not send one of its own. */
export const SIGNAL_HIDE_FLUSH_GAP_MS = 5000;
/** Marks the element whose pastes are watched, and names which it is. */
export const PASTE_TARGET_ATTRIBUTE = "data-paste-target";

/** Sends one batch; rejects with the failure. */
export type SendSignals = (events: Signal[], options: { keepalive: boolean }) => Promise<void>;

/**
 * Whether every later batch would be refused the same way, so the collector
 * stops. Not a refusal for the address: a laptop briefly on a hotspot is
 * outside the contest's network for a moment, and stopping would silence
 * monitoring until a reload.
 */
function isFinal(code: string): boolean {
  const kind = refusalKind(code);
  return kind === "closed" || kind === "excluded";
}

const PASTE_TARGETS: ReadonlySet<string> = new Set<PasteTarget>(["editor", "answer", "notes"]);

/** The watched field a paste landed in, or null when it is none of them. */
export function pasteTargetOf(node: EventTarget | null): PasteTarget | null {
  if (!(node instanceof Element)) return null;
  const marked = node.closest(`[${PASTE_TARGET_ATTRIBUTE}]`);
  const target = marked?.getAttribute(PASTE_TARGET_ATTRIBUTE) ?? null;
  return target !== null && PASTE_TARGETS.has(target) ? (target as PasteTarget) : null;
}

/** The first `max` UTF-16 units of text, without splitting a surrogate pair. */
function beginning(text: string, max: number): string {
  if (text.length <= max) return text;
  const cut = text.charCodeAt(max - 1);
  const end = cut >= 0xd800 && cut <= 0xdbff ? max - 1 : max;
  return text.slice(0, end);
}

const encoder = new TextEncoder();

export class SignalCollector {
  private readonly send: SendSignals;
  private buffer: Signal[] = [];
  /** When the current absence began, or null while the participant is here. */
  private awaySince: number | null = null;
  /** No ordinary send leaves before this time (a 429's Retry-After). */
  private quietUntil = 0;
  private inFlight = false;
  /** When the last batch left, or -Infinity before the first. */
  private lastSentAt = Number.NEGATIVE_INFINITY;
  private stopped = false;
  private detachListeners: (() => void) | null = null;

  constructor(send: SendSignals) {
    this.send = send;
  }

  /** What is waiting to be sent, oldest first. */
  pending(): readonly Signal[] {
    return this.buffer;
  }

  /** Starts listening and the send timer. Returns the detach. */
  attach(): () => void {
    if (this.stopped || this.detachListeners) return () => this.detach();

    const onVisibility = () => {
      if (document.visibilityState === "hidden") {
        this.leave();
        if (Date.now() - this.lastSentAt >= SIGNAL_HIDE_FLUSH_GAP_MS) this.flush(true);
      } else {
        this.back();
      }
    };
    const onBlur = () => this.leave();
    const onFocus = () => this.back();
    // The page going away ends an open absence: often the most telling one,
    // a participant who hid the tab and never came back to it.
    const onPageHide = (event: PageTransitionEvent) => {
      this.back();
      this.flush(true);
      // Kept in the back/forward cache: the page may come back, and the
      // time until it does is an absence like any other.
      if (event.persisted) this.leave();
    };
    const onPageShow = (event: PageTransitionEvent) => {
      if (event.persisted) this.back();
    };
    const onPaste = (event: Event) => this.paste(event as ClipboardEvent);
    const timer = setInterval(() => this.flush(false), SIGNAL_FLUSH_MS);

    document.addEventListener("visibilitychange", onVisibility);
    window.addEventListener("blur", onBlur);
    window.addEventListener("focus", onFocus);
    window.addEventListener("pagehide", onPageHide);
    window.addEventListener("pageshow", onPageShow);
    document.addEventListener("paste", onPaste, { capture: true, passive: true });

    this.detachListeners = () => {
      clearInterval(timer);
      document.removeEventListener("visibilitychange", onVisibility);
      window.removeEventListener("blur", onBlur);
      window.removeEventListener("focus", onFocus);
      window.removeEventListener("pagehide", onPageHide);
      window.removeEventListener("pageshow", onPageShow);
      document.removeEventListener("paste", onPaste, { capture: true });
    };
    return () => this.detach();
  }

  private detach() {
    this.detachListeners?.();
    this.detachListeners = null;
  }

  /** The contest is over for this participant: nothing more is collected or sent. */
  private stop() {
    this.stopped = true;
    this.buffer = [];
    this.detach();
  }

  private leave() {
    this.awaySince ??= Date.now();
  }

  private back() {
    if (this.awaySince === null) return;
    const now = Date.now();
    const away = now - this.awaySince;
    this.awaySince = null;
    if (away >= SIGNAL_MIN_AWAY_MS) {
      this.push({ kind: "page_left", client_at: new Date(now).toISOString(), away_ms: away });
    }
  }

  private paste(event: ClipboardEvent) {
    const target = pasteTargetOf(event.target);
    if (target === null) return;
    const text = event.clipboardData?.getData("text/plain") ?? "";
    if (text === "") return;
    this.push({
      kind: "paste",
      client_at: new Date().toISOString(),
      target,
      chars: text.length,
      text: beginning(text, SIGNAL_PASTE_TEXT_MAX),
    });
  }

  private push(signal: Signal) {
    if (this.stopped) return;
    this.buffer.push(signal);
    this.trim();
  }

  /** Drops the oldest signals past the cap. */
  private trim() {
    if (this.buffer.length > SIGNAL_BUFFER_MAX) {
      this.buffer.splice(0, this.buffer.length - SIGNAL_BUFFER_MAX);
    }
  }

  /** Takes the next batch off the front of the buffer. */
  private take(keepalive: boolean): Signal[] {
    if (!keepalive) return this.buffer.splice(0, SIGNAL_BATCH_MAX);
    // `{"events":[` + `]}` and a comma between each.
    let bytes = 13;
    let count = 0;
    while (count < this.buffer.length && count < SIGNAL_BATCH_MAX) {
      const size = encoder.encode(JSON.stringify(this.buffer[count])).length + 1;
      if (count > 0 && bytes + size > SIGNAL_KEEPALIVE_BYTES) break;
      bytes += size;
      count++;
    }
    return this.buffer.splice(0, count);
  }

  private flush(keepalive: boolean) {
    if (this.stopped || this.buffer.length === 0) return;
    if (Date.now() < this.quietUntil) return;
    // One ordinary send at a time; the one on the way out goes regardless,
    // with whatever the ordinary one did not take.
    if (this.inFlight && !keepalive) return;

    const batch = this.take(keepalive);
    this.lastSentAt = Date.now();
    if (!keepalive) this.inFlight = true;
    this.send(batch, { keepalive })
      .catch((error: unknown) => this.failed(batch, error))
      .finally(() => {
        if (!keepalive) this.inFlight = false;
      });
  }

  private failed(batch: Signal[], error: unknown) {
    if (this.stopped) return;
    if (error instanceof ApiError) {
      // A 401 is final too, whatever its code: the session has ended, and
      // retrying would hold the batch and send it every ten seconds forever.
      if (isFinal(error.code) || error.status === 401) {
        this.stop();
        return;
      }
      if (error.status === 429) {
        this.quietUntil = Date.now() + (error.retryAfterSeconds ?? SIGNAL_FLUSH_MS / 1000) * 1000;
      } else if (error.status < 500 && error.status !== 408) {
        // Refused as such: the same batch would be refused again.
        return;
      }
    }
    // Lost or deferred: back into the buffer in the order the signals
    // happened. Two batches in flight can fail in either order, so putting
    // this one at the front is not enough; a stable sort by the browser's own
    // time (ISO strings compare as times) restores it.
    this.buffer.unshift(...batch);
    this.buffer.sort((a, b) => (a.client_at < b.client_at ? -1 : a.client_at > b.client_at ? 1 : 0));
    this.trim();
  }
}

/**
 * Collects the play screen's signals for `contestId` while the screen is
 * mounted. Renders nothing and holds no React state.
 */
export function useSignals(contestId: string): void {
  useEffect(() => {
    const collector = new SignalCollector((events, options) => sendSignals(contestId, events, options));
    return collector.attach();
  }, [contestId]);
}
