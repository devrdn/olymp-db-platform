"use client";

import { useEffect, useMemo, useState, useSyncExternalStore } from "react";

import { ApiError } from "@/lib/api/client";

import { isClosed, type ClosedCode } from "./refusals";

/**
 * Saving without a button, for one document at a time: the participant's
 * notes, or one SQL tab (docs/ARCHITECTURE.md §6.4). `AutosaveEngine`
 * enforces:
 *
 * - a save leaves 1.5 s after the last edit, and at least every 10 s while
 *   the participant keeps typing;
 * - it leaves at once on `flush()` (an editor losing focus), and at once as
 *   a `keepalive` request when the tab is hidden (unless a retry is
 *   waiting), on `pagehide`, and when the editor unmounts with unsaved text:
 *   an ordinary fetch does not survive the page closing;
 * - text equal to what the server last confirmed is never sent;
 * - at most one ordinary request per document is in flight; edits made
 *   meanwhile leave after it answers;
 * - a network or 5xx failure is retried after 2, 4, 8, 16 and then every
 *   30 s; a 429 waits as long as `Retry-After` says;
 * - a refusal of the text itself (a 4xx such as `workspace_notes_too_long`)
 *   is not retried until the text changes;
 * - once the contest has closed for the participant (409 `contest_ended`,
 *   `contest_finished` or `deadline_passed`), the engine stops for good.
 *   `contest_not_running` cannot arrive mid-work, since a running contest
 *   never goes back to draft; if it does, it is an ordinary 4xx refusal.
 *
 * Until the server confirms a text, it is kept as a draft in `localStorage`,
 * keyed by account, contest and document, for a reload before the save
 * landed. The draft records the server version it was written against
 * (`base`) and fingerprints of the texts in flight (`sent`); on mount it is
 * restored if the server copy is still that version or one of those texts.
 * A copy saved later from elsewhere wins (last write wins). Client and server
 * clocks are never compared: lab machines are not trusted to agree.
 *
 * Every storage access is wrapped: denied or full storage costs only the
 * draft, never the save. React sees only the status, so typing re-renders
 * nothing unless the status changes.
 */

/** A save that is waiting, running, done, or will not happen. */
export type AutosaveStatus =
  /** The server holds exactly the text on screen. */
  | { kind: "saved" }
  /** Edited; the save has not left yet. */
  | { kind: "pending" }
  /** A save is in flight. */
  | { kind: "saving" }
  /** The last save failed and will be retried; the text is in the draft. */
  | { kind: "retrying" }
  /** The server refused this text (`code`); it is sent again only once it changes. */
  | { kind: "rejected"; code: string }
  /** The contest is over for this participant; nothing more will be saved. */
  | { kind: "closed"; code: ClosedCode };

export type AutosaveOptions = {
  /**
   * Who is typing. `null` when the account could not be read, and then no
   * draft is kept: a draft under a shared key could cost somebody their notes,
   * while losing it costs a reload's worth of typing.
   */
  accountId: string | null;
  contestId: string;
  /**
   * Which document this is (`notes`, `tab:{id}`). Fixed for the hook's life,
   * with `contestId`: key the component to change it.
   */
  documentKey: string;
  /** The text the server returned on the first read. */
  initialText: string;
  /** The server's `updated_at` for that text, or null if it was never saved. */
  initialVersion: string | null;
  /** Sends one text; resolves with the server's new `updated_at`, rejects with the failure. */
  save: (text: string, options: { keepalive: boolean }) => Promise<string>;
  /** Called on mount when a draft beats the server copy: the editor should show this text. */
  onRestore?: (text: string) => void;
};

export type Autosave = {
  status: AutosaveStatus;
  /** Tells the engine what the editor now holds. Cheap; call it on every edit. */
  setValue: (text: string) => void;
  /** Saves now, unless a retry is already waiting or there is nothing to save. */
  flush: () => void;
  /** Forgets the document: no more saves, and the draft is removed (a tab that was closed). */
  discard: () => void;
};

export const AUTOSAVE_DEBOUNCE_MS = 1500;
export const AUTOSAVE_MAX_WAIT_MS = 10_000;
export const AUTOSAVE_RETRY_FIRST_MS = 2000;
export const AUTOSAVE_RETRY_MAX_MS = 30_000;
/** How long the draft waits for typing to pause before it is written. */
export const AUTOSAVE_DRAFT_WRITE_MS = 300;

/** What every draft key starts with, whoever wrote it. */
const DRAFT_PREFIX = "dbcontest.play.draft.";

/**
 * One draft key per account, contest and document. The account is in the
 * key because lab machines are shared: two accounts that never saved both
 * stand on a null version, so without it a student would inherit, and
 * autosave, the previous user's draft.
 */
export function draftStorageKey(accountId: string, contestId: string, documentKey: string): string {
  return `${DRAFT_PREFIX}${encodeURIComponent(accountId)}.${contestId}.${documentKey}`;
}

/**
 * Removes every draft on this machine that belongs to another account, so a
 * previous user's text does not linger. Drafts of this account's other
 * contests are kept. Called once as the play screen mounts.
 */
export function purgeForeignDrafts(accountId: string): void {
  const store = storage();
  if (store === null) return;
  const mine = `${DRAFT_PREFIX}${encodeURIComponent(accountId)}.`;
  try {
    const doomed: string[] = [];
    for (let i = 0; i < store.length; i++) {
      const key = store.key(i);
      if (key !== null && key.startsWith(DRAFT_PREFIX) && !key.startsWith(mine)) doomed.push(key);
    }
    for (const key of doomed) store.removeItem(key);
  } catch {
    // A private window or a denied policy: there is nothing stored to sweep.
  }
}

/**
 * Length plus a 32-bit FNV-1a hash of the UTF-16 code units: enough to tell
 * that the server holds the text that was in flight, without storing it twice.
 */
export function textFingerprint(text: string): string {
  let hash = 0x811c9dc5;
  for (let i = 0; i < text.length; i++) {
    hash ^= text.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193);
  }
  return `${text.length}:${(hash >>> 0).toString(16)}`;
}

type Draft = {
  text: string;
  /** The server version this text was edited on top of. */
  base: string | null;
  /**
   * Fingerprints of texts sent but not yet confirmed. The server holding one
   * means that save landed and later edits did not, so the draft is newer.
   */
  sent?: string[];
};

function parseDraft(raw: string | null): Draft | null {
  if (raw === null) return null;
  try {
    const value: unknown = JSON.parse(raw);
    if (typeof value !== "object" || value === null) return null;
    const { text, base, sent } = value as Record<string, unknown>;
    if (typeof text !== "string") return null;
    if (base !== null && typeof base !== "string") return null;
    if (sent !== undefined && !(Array.isArray(sent) && sent.every((one) => typeof one === "string"))) return null;
    return { text, base, sent: sent as string[] | undefined };
  } catch {
    return null;
  }
}

/** `window.localStorage`, or null where merely reaching it throws. */
function storage(): Storage | null {
  try {
    return typeof window === "undefined" ? null : window.localStorage;
  } catch {
    return null;
  }
}

type Timer = ReturnType<typeof setTimeout>;

const SAVED: AutosaveStatus = { kind: "saved" };
const PENDING: AutosaveStatus = { kind: "pending" };
const SAVING: AutosaveStatus = { kind: "saving" };
const RETRYING: AutosaveStatus = { kind: "retrying" };

/**
 * The state machine behind `useAutosave`, free of React so the SQL tabs can
 * hold one per tab. Methods are bound properties, safe to pass as handlers.
 */
export class AutosaveEngine {
  private options: AutosaveOptions;
  private readonly key: string | null;

  private text: string;
  /**
   * What the server is known to hold, and its version. Null means unknown:
   * two requests overlapped, and neither answer proves which committed last.
   */
  private saved: string | null;
  private version: string | null;
  private readonly flights: { text: string }[] = [];
  /** Two requests for this document were in flight at the same time. */
  private overlapped = false;
  /** Fingerprints of texts sent whose effect on the server is unconfirmed. */
  private unconfirmed: string[] = [];
  /** The last text sent as a keepalive request, so leaving twice sends once. */
  private lastKeepalive: string | null = null;
  private sendWhenIdle = false;
  private rejected: { text: string; code: string } | null = null;
  private closedCode: ClosedCode | null = null;
  private discarded = false;
  /** Before mount, and between an unmount and a remount (strict mode runs both). */
  private suspended = true;
  private recovered = false;
  /** Failed attempts since the last success, for the backoff. */
  private attempt = 0;

  private debounceTimer: Timer | undefined;
  private maxWaitTimer: Timer | undefined;
  private retryTimer: Timer | undefined;
  private draftTimer: Timer | undefined;

  private status: AutosaveStatus = SAVED;
  private readonly listeners = new Set<() => void>();

  constructor(options: AutosaveOptions) {
    this.options = options;
    this.key =
      options.accountId === null
        ? null
        : draftStorageKey(options.accountId, options.contestId, options.documentKey);
    this.text = options.initialText;
    this.saved = options.initialText;
    this.version = options.initialVersion;
  }

  /** Takes the caller's latest callbacks; the document does not change. */
  setCallbacks = (options: Pick<AutosaveOptions, "save" | "onRestore">) => {
    this.options = { ...this.options, save: options.save, onRestore: options.onRestore };
  };

  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  getStatus = () => this.status;

  /** Mounted: recover the draft once and pick up anything unsent. */
  start = () => {
    this.suspended = false;
    if (!this.recovered) {
      this.recovered = true;
      this.recover();
    }
    this.scheduleIfDirty();
    this.emit();
  };

  /** Unmounted: send what is unsaved and stop every timer. */
  suspend = () => {
    this.leave();
    this.suspended = true;
    this.clearTimers();
  };

  setValue = (text: string) => {
    if (this.discarded) return;
    this.text = text;
    this.storeDraft();
    if (this.closedCode !== null) return;

    if (text === this.saved || text === this.rejected?.text) {
      clearTimeout(this.debounceTimer);
      clearTimeout(this.maxWaitTimer);
      this.debounceTimer = this.maxWaitTimer = undefined;
      if (text === this.saved && this.flights.length === 0) {
        clearTimeout(this.retryTimer);
        this.retryTimer = undefined;
        this.attempt = 0;
      }
    } else if (this.retryTimer === undefined) {
      // A waiting retry sends the latest text when it fires; a new edit
      // must not cut the pause short.
      clearTimeout(this.debounceTimer);
      this.debounceTimer = setTimeout(this.due, AUTOSAVE_DEBOUNCE_MS);
      this.maxWaitTimer ??= setTimeout(this.due, AUTOSAVE_MAX_WAIT_MS);
    }
    this.emit();
  };

  flush = () => {
    this.send({ keepalive: false, bypassWait: false });
  };

  /**
   * The tab was hidden. The page is still there, so a server-requested pause
   * is honoured: every refused write counts against the per-minute budget.
   */
  hide = () => {
    this.storeDraft(true);
    this.send({ keepalive: true, bypassWait: false });
  };

  /**
   * The page is going away (`pagehide`, or unmount). Last chance to send, so a
   * waiting retry does not hold it back.
   */
  leave = () => {
    this.storeDraft(true);
    this.send({ keepalive: true, bypassWait: true });
  };

  discard = () => {
    this.discarded = true;
    this.clearTimers();
    this.removeDraft();
  };

  private due = () => {
    this.send({ keepalive: false, bypassWait: false });
  };

  private send({ keepalive, bypassWait }: { keepalive: boolean; bypassWait: boolean }) {
    clearTimeout(this.debounceTimer);
    clearTimeout(this.maxWaitTimer);
    this.debounceTimer = this.maxWaitTimer = undefined;
    if (this.discarded || this.suspended || this.closedCode !== null) return;

    const text = this.text;
    if (text === this.saved || text === this.rejected?.text) {
      this.emit();
      return;
    }
    // A server-requested or earned pause is waited out by everything except the
    // page going away.
    if (this.retryTimer !== undefined && !bypassWait) return;

    if (this.flights.length > 0) {
      if (this.flights.some((flight) => flight.text === text)) return;
      // An ordinary save waits for the answer. A page going away cannot, so it
      // sends in parallel, and then neither answer counts as proof
      // (`settleFlight`).
      if (!keepalive) {
        this.sendWhenIdle = true;
        return;
      }
    }
    if (keepalive && text === this.lastKeepalive) return;
    this.run(text, keepalive);
  }

  private run(text: string, keepalive: boolean) {
    clearTimeout(this.retryTimer);
    this.retryTimer = undefined;
    if (this.flights.length > 0) this.overlapped = true;
    const flight = { text };
    this.flights.push(flight);
    if (keepalive) this.lastKeepalive = text;
    const print = textFingerprint(text);
    if (!this.unconfirmed.includes(print)) this.unconfirmed.push(print);
    this.storeDraft(keepalive);
    this.emit();

    let request: Promise<string>;
    try {
      request = this.options.save(text, { keepalive });
    } catch (error) {
      request = Promise.reject(error);
    }
    request.then(
      (version) => this.succeeded(text, version, flight),
      (error: unknown) => this.failed(text, error, flight, keepalive),
    );
  }

  /**
   * Removes a request from flight and reports whether its answer can be
   * believed. Not if another request overlapped it: the commit order is
   * unknown until one more save settles it.
   */
  private settleFlight(flight: { text: string }): boolean {
    const at = this.flights.indexOf(flight);
    if (at >= 0) this.flights.splice(at, 1);
    if (!this.overlapped) return true;
    if (this.flights.length === 0) {
      // Both answered and nothing is confirmed: send the current text once
      // more to make the server's copy known.
      this.overlapped = false;
      this.saved = null;
      this.sendWhenIdle = true;
    }
    return false;
  }

  private succeeded(text: string, version: string, flight: { text: string }) {
    const believable = this.settleFlight(flight);
    if (this.discarded) return;
    this.attempt = 0;
    // Keep the version either way: it is the draft's base, and newer is
    // closer to the truth.
    this.version = version;
    if (believable) {
      this.saved = text;
      this.unconfirmed = [];
    }
    this.storeDraft();
    this.settle();
  }

  private failed(text: string, error: unknown, flight: { text: string }, keepalive: boolean) {
    this.settleFlight(flight);
    // A failed keepalive did not reach the server, so leaving again may
    // resend this text.
    if (keepalive && this.lastKeepalive === text) this.lastKeepalive = null;
    if (this.discarded) return;

    if (error instanceof ApiError && isClosed(error.code)) {
      this.closedCode = error.code;
      this.clearTimers();
      this.sendWhenIdle = false;
      this.storeDraft(true);
      this.emit();
      return;
    }

    if (error instanceof ApiError && error.status === 429) {
      // Never shorter than the first pause: refused saves count against the
      // same budget, so a zero wait must not become a loop.
      const wait =
        error.retryAfterSeconds !== undefined
          ? Math.max(error.retryAfterSeconds * 1000, AUTOSAVE_RETRY_FIRST_MS)
          : this.pause();
      this.retryLater(wait);
    } else if (error instanceof ApiError && isRefusalOfText(error.status)) {
      this.rejected = { text, code: error.code };
    } else {
      // The network, a 5xx, an expired session: not about the text, and may
      // pass.
      this.retryLater(this.pause());
    }
    this.settle();
  }

  /** After a request answered: send what came due meanwhile, then report. */
  private settle() {
    if (this.flights.length === 0 && this.closedCode === null) {
      if (this.retryTimer !== undefined) {
        this.sendWhenIdle = false;
      } else if (this.sendWhenIdle) {
        this.sendWhenIdle = false;
        this.send({ keepalive: false, bypassWait: false });
      } else {
        this.scheduleIfDirty();
      }
    }
    this.emit();
  }

  private pause(): number {
    return Math.min(AUTOSAVE_RETRY_FIRST_MS * 2 ** this.attempt, AUTOSAVE_RETRY_MAX_MS);
  }

  private retryLater(wait: number) {
    this.attempt++;
    clearTimeout(this.retryTimer);
    clearTimeout(this.debounceTimer);
    clearTimeout(this.maxWaitTimer);
    this.debounceTimer = this.maxWaitTimer = undefined;
    if (this.suspended) return;
    this.retryTimer = setTimeout(() => {
      this.retryTimer = undefined;
      this.send({ keepalive: false, bypassWait: false });
    }, wait);
  }

  /** Gives unsaved text with nothing pending the ordinary wait. */
  private scheduleIfDirty() {
    if (this.suspended || this.discarded || this.closedCode !== null) return;
    if (this.flights.length > 0 || this.retryTimer !== undefined || this.debounceTimer !== undefined) return;
    if (this.text === this.saved || this.text === this.rejected?.text) return;
    this.debounceTimer = setTimeout(this.due, AUTOSAVE_DEBOUNCE_MS);
    this.maxWaitTimer ??= setTimeout(this.due, AUTOSAVE_MAX_WAIT_MS);
  }

  private recover() {
    const draft = this.readDraft();
    if (draft === null) return;
    const { initialText, initialVersion } = this.options;
    if (draft.text === initialText) {
      this.removeDraft();
      return;
    }
    const newer =
      draft.base === initialVersion || (draft.sent?.includes(textFingerprint(initialText)) ?? false);
    if (!newer) {
      this.removeDraft();
      return;
    }
    this.text = draft.text;
    this.options.onRestore?.(draft.text);
    this.send({ keepalive: false, bypassWait: false });
  }

  private clearTimers() {
    clearTimeout(this.debounceTimer);
    clearTimeout(this.maxWaitTimer);
    clearTimeout(this.retryTimer);
    clearTimeout(this.draftTimer);
    this.debounceTimer = this.maxWaitTimer = this.retryTimer = this.draftTimer = undefined;
  }

  private computeStatus(): AutosaveStatus {
    if (this.closedCode !== null) return { kind: "closed", code: this.closedCode };
    if (this.flights.length > 0) return SAVING;
    if (this.text === this.saved) return SAVED;
    if (this.rejected !== null && this.text === this.rejected.text) return { kind: "rejected", code: this.rejected.code };
    if (this.retryTimer !== undefined) return RETRYING;
    return PENDING;
  }

  /** Publishes the status only when it changed: every publish is a render. */
  private emit() {
    const next = this.computeStatus();
    const current = this.status;
    if (next.kind === current.kind && ("code" in next ? next.code : "") === ("code" in current ? current.code : "")) {
      return;
    }
    this.status = next;
    for (const listener of this.listeners) listener();
  }

  /**
   * Removes the draft when the server holds the text, writes it otherwise.
   * Written after a short pause, since a SQL tab holds up to 64 KiB; `now` is
   * for moments with no later (tab hidden, page leaving, contest closed).
   */
  private storeDraft(now = false) {
    if (this.discarded) return;
    if (this.text === this.saved && this.flights.length === 0) {
      clearTimeout(this.draftTimer);
      this.draftTimer = undefined;
      this.removeDraft();
      return;
    }
    if (now) {
      clearTimeout(this.draftTimer);
      this.draftTimer = undefined;
      this.writeDraft();
      return;
    }
    this.draftTimer ??= setTimeout(() => {
      this.draftTimer = undefined;
      this.storeDraft(true);
    }, AUTOSAVE_DRAFT_WRITE_MS);
  }

  private writeDraft() {
    if (this.key === null) return;
    const draft: Draft = { text: this.text, base: this.version };
    if (this.unconfirmed.length > 0) draft.sent = [...this.unconfirmed];
    try {
      storage()?.setItem(this.key, JSON.stringify(draft));
    } catch {
      // The draft is a convenience; the save still goes.
    }
  }

  private readDraft(): Draft | null {
    if (this.key === null) return null;
    try {
      return parseDraft(storage()?.getItem(this.key) ?? null);
    } catch {
      return null;
    }
  }

  private removeDraft() {
    if (this.key === null) return;
    try {
      storage()?.removeItem(this.key);
    } catch {
      // Ignored on the next mount unless it still beats the server copy.
    }
  }
}

/**
 * A 4xx about the request itself, so the same text would be refused again.
 * 401 (the session may be restored), 408 and 429 are not; 409 closure codes
 * are handled before this is asked.
 */
function isRefusalOfText(status: number): boolean {
  return status >= 400 && status < 500 && status !== 401 && status !== 408 && status !== 429;
}

function serverStatus(): AutosaveStatus {
  return SAVED;
}

/**
 * Starts an engine and wires it to the tab-hidden and `pagehide` events. The
 * returned cleanup detaches them and sends what is unsaved. Exported for the
 * SQL tabs, which hold one engine per tab and cannot use a hook.
 */
export function attachEngine(engine: AutosaveEngine): () => void {
  const onVisibility = () => {
    if (document.visibilityState === "hidden") engine.hide();
  };
  engine.start();
  document.addEventListener("visibilitychange", onVisibility);
  window.addEventListener("pagehide", engine.leave);
  return () => {
    document.removeEventListener("visibilitychange", onVisibility);
    window.removeEventListener("pagehide", engine.leave);
    engine.suspend();
  };
}

/**
 * One autosaved document, for a component that holds exactly one (the
 * notes). The editor stays uncontrolled: call `setValue` on every edit and
 * apply `onRestore`'s text to it when a draft wins.
 */
export function useAutosave(options: AutosaveOptions): Autosave {
  const [engine] = useState(() => new AutosaveEngine(options));
  const { save, onRestore } = options;

  useEffect(() => {
    engine.setCallbacks({ save, onRestore });
  }, [engine, save, onRestore]);

  useEffect(() => attachEngine(engine), [engine]);

  const status = useSyncExternalStore(engine.subscribe, engine.getStatus, serverStatus);

  return useMemo(
    () => ({ status, setValue: engine.setValue, flush: engine.flush, discard: engine.discard }),
    [status, engine],
  );
}
