"use client";

import { useEffect, useMemo, useState, useSyncExternalStore } from "react";

import { ApiError } from "@/lib/api/client";

/**
 * Saving without a button, for one document at a time: the participant's
 * notes, or one SQL tab (docs/superpowers/specs/2026-09-17-play-workspace-design.md, §1, §2).
 *
 * The rules, all enforced by `AutosaveEngine` below:
 *
 * - a save leaves 1.5 s after the last edit, and at least every 10 s while
 *   the participant keeps typing;
 * - it leaves at once on `flush()` (an editor losing focus), and at once,
 *   as a `keepalive` request, when the browser tab is hidden, when the page
 *   is hidden for good (`pagehide`), and when the editor unmounts with
 *   unsaved text — a server action or an ordinary fetch does not survive
 *   the page closing;
 * - text equal to what the server last confirmed is never sent;
 * - at most one ordinary request per document is in flight; edits made
 *   while it runs leave in the next request, after it answers;
 * - a save that failed on the network or with a 5xx is retried after 2, 4,
 *   8, 16 and then every 30 s; a 429 waits as long as `Retry-After` says;
 * - a refusal of the text itself (a 4xx such as `workspace_notes_too_long`)
 *   is not retried until the text changes;
 * - once the contest has closed for the participant (409
 *   `contest_not_running` or `contest_finished`), the engine stops for good
 *   and says so; there is no read-only mode to fall back to.
 *
 * Until the server has confirmed a text, it is kept as a draft in
 * `localStorage`, keyed by contest and document, for the one case the
 * server cannot cover: the page reloaded before the save left or landed.
 * The draft records the server version it was written against (`base`) and
 * a fingerprint of the text in flight (`sent`); on mount it is shown and
 * saved again if the server copy is still that version, or is the text that
 * was in flight — the save landed, but the edits after it did not. A server
 * copy saved later from somewhere else wins, which is the last-write-wins
 * rule the whole workspace follows. Client and server clocks are never
 * compared: the computers in a lab are not trusted to agree on the time.
 *
 * Every storage access is wrapped: a private window, a full quota or a
 * policy that denies storage only costs the draft, never the save.
 *
 * React sees only the status. The text lives in the engine and in the
 * editor, so typing re-renders nothing unless the status changes.
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

export type ClosedCode = "contest_finished" | "contest_not_running";

export type AutosaveOptions = {
  contestId: string;
  /**
   * Which document of the contest this is (`notes`, `tab:{id}`). Fixed for
   * the life of the hook, together with `contestId`: a different document
   * is a different component instance (key it).
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

const CLOSED_CODES: ReadonlySet<string> = new Set<ClosedCode>(["contest_finished", "contest_not_running"]);

/** Where a document's draft is kept. One key per contest and document. */
export function draftStorageKey(contestId: string, documentKey: string): string {
  return `dbcontest.play.draft.${contestId}.${documentKey}`;
}

/**
 * A short fingerprint of a text: its length and a 32-bit FNV-1a hash of its
 * UTF-16 code units. Enough to recognise "the server holds the text that was
 * in flight" without storing that text a second time.
 */
export function textFingerprint(text: string): string {
  let hash = 0x811c9dc5;
  for (let i = 0; i < text.length; i++) {
    hash ^= text.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193);
  }
  return `${text.length}:${(hash >>> 0).toString(16)}`;
}

type Draft = { text: string; base: string | null; sent?: string };

function parseDraft(raw: string | null): Draft | null {
  if (raw === null) return null;
  try {
    const value: unknown = JSON.parse(raw);
    if (typeof value !== "object" || value === null) return null;
    const { text, base, sent } = value as Record<string, unknown>;
    if (typeof text !== "string") return null;
    if (base !== null && typeof base !== "string") return null;
    if (sent !== undefined && typeof sent !== "string") return null;
    return { text, base, sent };
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
 * The state machine behind `useAutosave`, free of React so Task 4's editor
 * can hold one per SQL tab without a hook per tab. Methods are bound
 * properties: they are handed to event handlers as they are.
 */
export class AutosaveEngine {
  private options: AutosaveOptions;
  private readonly key: string;

  /** What the editor holds. */
  private text: string;
  /** What the server last confirmed, and its version. */
  private saved: string;
  private version: string | null;
  /** The text of the ordinary request in flight, if any. */
  private inFlight: string | null = null;
  /** A save came due while a request was in flight. */
  private sendWhenIdle = false;
  /** The text the server refused, and why. */
  private rejected: { text: string; code: string } | null = null;
  private closedCode: ClosedCode | null = null;
  private discarded = false;
  /** Between unmount and a remount (React's strict mode runs both). */
  private suspended = true;
  private recovered = false;
  /** Failed attempts since the last success, for the pause. */
  private attempt = 0;

  private debounceTimer: Timer | undefined;
  private maxWaitTimer: Timer | undefined;
  private retryTimer: Timer | undefined;

  private status: AutosaveStatus = SAVED;
  private readonly listeners = new Set<() => void>();

  constructor(options: AutosaveOptions) {
    this.options = options;
    this.key = draftStorageKey(options.contestId, options.documentKey);
    this.text = options.initialText;
    this.saved = options.initialText;
    this.version = options.initialVersion;
  }

  /** Takes the caller's latest callbacks; the document itself does not change. */
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

  /** Mounted: recover the draft once, and pick up anything left unsent. */
  start = () => {
    this.suspended = false;
    if (!this.recovered) {
      this.recovered = true;
      this.recover();
    }
    this.scheduleIfDirty();
    this.emit();
  };

  /** Unmounted: send what is unsaved on the way out, and stop every timer. */
  suspend = () => {
    this.hide();
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
      if (text === this.saved && this.inFlight === null) {
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
    this.send(false);
  };

  /** The tab was hidden or the page is going away: send now, in a request that outlives it. */
  hide = () => {
    this.send(true);
  };

  discard = () => {
    this.discarded = true;
    this.clearTimers();
    this.removeDraft();
  };

  private due = () => {
    this.send(false);
  };

  private send(keepalive: boolean) {
    clearTimeout(this.debounceTimer);
    clearTimeout(this.maxWaitTimer);
    this.debounceTimer = this.maxWaitTimer = undefined;
    if (this.discarded || this.suspended || this.closedCode !== null) return;

    const text = this.text;
    if (this.inFlight !== null) {
      // The page may be going away, and the request in flight carries older
      // text: waiting for it would lose the rest. Otherwise the next save
      // leaves when this one answers. Should the older request land last,
      // the text it confirms differs from the editor's, and the engine sends
      // the newer text again — which also corrects the server.
      if (keepalive && text !== this.inFlight && text !== this.rejected?.text) {
        this.run(text, true);
      } else {
        this.sendWhenIdle = true;
      }
      return;
    }
    if (text === this.saved || text === this.rejected?.text) {
      this.emit();
      return;
    }
    // An explicit flush respects a waiting retry; leaving the page does not.
    if (this.retryTimer !== undefined && !keepalive) return;
    this.run(text, keepalive);
  }

  private run(text: string, keepalive: boolean) {
    clearTimeout(this.retryTimer);
    this.retryTimer = undefined;
    const owner = this.inFlight === null;
    if (owner) this.inFlight = text;
    this.storeDraft();
    this.emit();

    let request: Promise<string>;
    try {
      request = this.options.save(text, { keepalive });
    } catch (error) {
      request = Promise.reject(error);
    }
    request.then(
      (version) => this.succeeded(text, version, owner),
      (error: unknown) => this.failed(text, error, owner),
    );
  }

  private succeeded(text: string, version: string, owner: boolean) {
    if (owner) this.inFlight = null;
    if (this.discarded) return;
    this.saved = text;
    this.version = version;
    this.attempt = 0;
    this.storeDraft();
    this.settle();
  }

  private failed(text: string, error: unknown, owner: boolean) {
    if (owner) this.inFlight = null;
    if (this.discarded) return;

    if (error instanceof ApiError && CLOSED_CODES.has(error.code)) {
      this.closedCode = error.code as ClosedCode;
      this.clearTimers();
      this.sendWhenIdle = false;
      this.storeDraft();
      this.emit();
      return;
    }

    if (error instanceof ApiError && error.status === 429) {
      const wait = error.retryAfterSeconds !== undefined ? error.retryAfterSeconds * 1000 : this.pause();
      this.retryLater(wait);
    } else if (error instanceof ApiError && isRefusalOfText(error.status)) {
      this.rejected = { text, code: error.code };
    } else {
      // The network, a 5xx, an expired session: none is about the text, and
      // any may pass.
      this.retryLater(this.pause());
    }
    this.settle();
  }

  /** After a request answered: send what came due meanwhile, then report. */
  private settle() {
    if (this.inFlight === null && this.closedCode === null) {
      if (this.retryTimer !== undefined) {
        this.sendWhenIdle = false;
      } else if (this.sendWhenIdle) {
        this.sendWhenIdle = false;
        this.send(false);
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
      this.send(false);
    }, wait);
  }

  /** Unsaved text with nothing on the way to send it gets the ordinary wait. */
  private scheduleIfDirty() {
    if (this.suspended || this.discarded || this.closedCode !== null) return;
    if (this.inFlight !== null || this.retryTimer !== undefined || this.debounceTimer !== undefined) return;
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
      draft.base === initialVersion || (draft.sent !== undefined && draft.sent === textFingerprint(initialText));
    if (!newer) {
      this.removeDraft();
      return;
    }
    this.text = draft.text;
    this.options.onRestore?.(draft.text);
    this.send(false);
  }

  private clearTimers() {
    clearTimeout(this.debounceTimer);
    clearTimeout(this.maxWaitTimer);
    clearTimeout(this.retryTimer);
    this.debounceTimer = this.maxWaitTimer = this.retryTimer = undefined;
  }

  private computeStatus(): AutosaveStatus {
    if (this.closedCode !== null) return { kind: "closed", code: this.closedCode };
    if (this.inFlight !== null) return SAVING;
    if (this.text === this.saved) return SAVED;
    if (this.rejected !== null && this.text === this.rejected.text) return { kind: "rejected", code: this.rejected.code };
    if (this.retryTimer !== undefined) return RETRYING;
    return PENDING;
  }

  /** Publishes the status, and only when it changed: every publish is a render. */
  private emit() {
    const next = this.computeStatus();
    const current = this.status;
    if (next.kind === current.kind && ("code" in next ? next.code : "") === ("code" in current ? current.code : "")) {
      return;
    }
    this.status = next;
    for (const listener of this.listeners) listener();
  }

  /** Keeps the draft in step: removed when the server holds the text, written otherwise. */
  private storeDraft() {
    if (this.discarded) return;
    if (this.text === this.saved && this.inFlight === null) {
      this.removeDraft();
      return;
    }
    const draft: Draft = { text: this.text, base: this.version };
    if (this.inFlight !== null) draft.sent = textFingerprint(this.inFlight);
    try {
      storage()?.setItem(this.key, JSON.stringify(draft));
    } catch {
      // Quota or policy: the draft is a convenience, the save still goes.
    }
  }

  private readDraft(): Draft | null {
    try {
      return parseDraft(storage()?.getItem(this.key) ?? null);
    } catch {
      return null;
    }
  }

  private removeDraft() {
    try {
      storage()?.removeItem(this.key);
    } catch {
      // Nothing to do: a draft that cannot be removed is ignored on the next
      // mount unless it still beats the server copy.
    }
  }
}

/**
 * A 4xx that is about the request itself, so sending the same text again
 * would be refused again. 401 (a session that may be restored), 408 and 429
 * are not; 409 closure codes are handled before this is asked.
 */
function isRefusalOfText(status: number): boolean {
  return status >= 400 && status < 500 && status !== 401 && status !== 408 && status !== 429;
}

function serverStatus(): AutosaveStatus {
  return SAVED;
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

  useEffect(() => {
    const onVisibility = () => {
      if (document.visibilityState === "hidden") engine.hide();
    };
    engine.start();
    document.addEventListener("visibilitychange", onVisibility);
    window.addEventListener("pagehide", engine.hide);
    return () => {
      document.removeEventListener("visibilitychange", onVisibility);
      window.removeEventListener("pagehide", engine.hide);
      engine.suspend();
    };
  }, [engine]);

  const status = useSyncExternalStore(engine.subscribe, engine.getStatus, serverStatus);

  return useMemo(
    () => ({ status, setValue: engine.setValue, flush: engine.flush, discard: engine.discard }),
    [status, engine],
  );
}
