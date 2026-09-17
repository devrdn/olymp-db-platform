"use client";

import { useId, useRef, useState } from "react";

import { MAX_TABS, TAB_TITLE_MAX_CHARS } from "@/lib/api/workspace";
import { cn } from "@/lib/utils";

import type { PlayDictionary } from "./dictionary";
import type { AutosaveStatus } from "./use-autosave";

/** One tab, as the strip needs it: what it is called and which document it is. */
export type SqlTabView = { id: string; title: string };

/**
 * The strip above the SQL editor (§5 of the workspace design): a tab per
 * document, a ✕ on each, a + at the end.
 *
 * It draws and it reports; it decides nothing. Whether closing a tab needs a
 * confirmation, what a new tab is called, whether a new order reached the
 * server — all of that belongs to `use-sql-tabs.ts`, which is where the text
 * of each tab and the server are. Keeping this half free of both is what
 * lets every key and every role below be tested without a network at all.
 *
 * The keyboard is the same one a `tablist` promises — arrows between the
 * tabs, Home and End to the ends, one stop in the page's own tab order — plus
 * the two an editor's tabs need and a tablist does not describe: F2 renames,
 * and Ctrl/⌘+Shift+Arrow moves the tab itself, so a strip that can be
 * arranged by dragging can be arranged without a pointer too.
 *
 * `closed` is the contest having ended for this participant. Every write is
 * refused from then on, so nothing that writes is offered: no +, no ✕, no
 * rename, no drag. Switching tabs and reading them stays.
 */
export function SqlTabStrip({
  tabs,
  activeId,
  idPrefix,
  panelId,
  closed,
  status,
  dict,
  onSelect,
  onCreate,
  onRename,
  onClose,
  onMove,
}: {
  tabs: SqlTabView[];
  activeId: string;
  /** Prefix for each tab's DOM id, so the editor below can name the tab it belongs to. */
  idPrefix: string;
  /** The editor's own element, which this strip's tabs control. */
  panelId: string;
  /** The contest is over: the strip is read-only. */
  closed: boolean;
  /** The active tab's save status, at the end of the strip. */
  status?: React.ReactNode;
  dict: PlayDictionary;
  onSelect: (id: string) => void;
  onCreate: () => void;
  /** A new name for a tab. Refusals are the caller's to report — the server has the last word on a title. */
  onRename: (id: string, title: string) => void;
  onClose: (id: string) => void;
  /** The tab `id` belongs at index `to` of the strip. */
  onMove: (id: string, to: number) => void;
}) {
  const t = dict.participant.play.workspace.editor;
  const limitId = useId();
  const elements = useRef(new Map<string, HTMLElement | null>());
  const dragged = useRef<string | null>(null);
  const [renaming, setRenaming] = useState<string | null>(null);
  /** Set while Esc is taking a rename back, so the blur it causes does not save it. */
  const cancelled = useRef(false);

  const full = tabs.length >= MAX_TABS;
  const closable = tabs.length > 1 && !closed;

  const select = (id: string) => {
    onSelect(id);
    // Focus follows selection within the strip, which is what makes the
    // arrows usable: the element is already on screen, so this does not wait
    // for the parent's own re-render.
    elements.current.get(id)?.focus();
  };

  const startRename = (id: string) => {
    if (!closed) setRenaming(id);
  };

  const finishRename = (tab: SqlTabView, value: string) => {
    setRenaming(null);
    // An unchanged name is not a write. Every write counts against the
    // participant's per-minute budget, refused or not.
    if (value !== tab.title) onRename(tab.id, value);
  };

  const onTabKeyDown = (event: React.KeyboardEvent, tab: SqlTabView, index: number) => {
    const at = (position: number) => tabs[(position + tabs.length) % tabs.length].id;
    if ((event.ctrlKey || event.metaKey) && event.shiftKey) {
      if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
      event.preventDefault();
      const to = index + (event.key === "ArrowLeft" ? -1 : 1);
      if (closed || to < 0 || to >= tabs.length) return;
      onMove(tab.id, to);
      return;
    }
    switch (event.key) {
      case "ArrowLeft":
        event.preventDefault();
        select(at(index - 1));
        break;
      case "ArrowRight":
        event.preventDefault();
        select(at(index + 1));
        break;
      case "Home":
        event.preventDefault();
        select(tabs[0].id);
        break;
      case "End":
        event.preventDefault();
        select(tabs[tabs.length - 1].id);
        break;
      case "F2":
        event.preventDefault();
        startRename(tab.id);
        break;
      case "Delete":
      case "Backspace":
        event.preventDefault();
        if (closable) onClose(tab.id);
        break;
      case "Enter":
      case " ":
        event.preventDefault();
        onSelect(tab.id);
        break;
    }
  };

  return (
    <div className="flex shrink-0 items-center gap-2 border-b border-line pr-3">
      <div
        role="tablist"
        aria-label={t.tablist}
        aria-orientation="horizontal"
        className="flex min-w-0 flex-1 items-stretch overflow-x-auto"
      >
        {tabs.map((tab, index) => {
          const active = tab.id === activeId;
          return (
            <div
              key={tab.id}
              ref={(element) => {
                elements.current.set(tab.id, element);
              }}
              role="tab"
              id={`${idPrefix}${tab.id}`}
              // The name is the title and nothing else: the ✕ inside carries
              // a label of its own, and without this the tab would be
              // announced as "Suspects, Close Suspects".
              aria-label={tab.title}
              aria-selected={active}
              aria-controls={panelId}
              tabIndex={active ? 0 : -1}
              draggable={!closed && renaming !== tab.id}
              onDragStart={() => {
                dragged.current = tab.id;
              }}
              onDragOver={(event) => {
                if (dragged.current !== null) event.preventDefault();
              }}
              onDrop={(event) => {
                event.preventDefault();
                const from = dragged.current;
                dragged.current = null;
                if (from !== null && from !== tab.id) onMove(from, index);
              }}
              onDragEnd={() => {
                dragged.current = null;
              }}
              onClick={() => onSelect(tab.id)}
              onDoubleClick={() => startRename(tab.id)}
              onKeyDown={(event) => onTabKeyDown(event, tab, index)}
              className={cn(
                "flex min-w-0 shrink-0 cursor-pointer items-center gap-1.5 border-r border-line px-3 py-1.5",
                "text-control-sm transition-colors duration-(--t-input) ease-standard",
                "focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent-ink",
                active ? "bg-sunk text-ink" : "text-ink-2 hover:text-ink",
              )}
            >
              {renaming === tab.id ? (
                <input
                  autoFocus
                  aria-label={t.rename.replace("{tab}", tab.title)}
                  defaultValue={tab.title}
                  // The server's own bound on a title, so the ordinary
                  // typing path stops where the refusal would start. An
                  // empty name, or one pasted past this, is still the
                  // server's to refuse.
                  maxLength={TAB_TITLE_MAX_CHARS}
                  spellCheck={false}
                  size={Math.max(4, tab.title.length)}
                  onClick={(event) => event.stopPropagation()}
                  onDoubleClick={(event) => event.stopPropagation()}
                  onKeyDown={(event) => {
                    event.stopPropagation();
                    if (event.key === "Enter") {
                      event.preventDefault();
                      finishRename(tab, event.currentTarget.value);
                    } else if (event.key === "Escape") {
                      event.preventDefault();
                      cancelled.current = true;
                      setRenaming(null);
                    }
                  }}
                  onBlur={(event) => {
                    if (cancelled.current) {
                      cancelled.current = false;
                      return;
                    }
                    finishRename(tab, event.currentTarget.value);
                  }}
                  className="min-w-0 border border-edge bg-bg px-1 font-sans text-control-sm text-ink outline-none"
                />
              ) : (
                <span className="truncate">{tab.title}</span>
              )}
              {closable ? (
                <button
                  type="button"
                  // Not its own stop in the tab order: the strip is one stop,
                  // and Delete on the focused tab is the keyboard's way to
                  // this button.
                  tabIndex={-1}
                  aria-label={t.close.replace("{tab}", tab.title)}
                  onClick={(event) => {
                    event.stopPropagation();
                    onClose(tab.id);
                  }}
                  className="shrink-0 px-1 text-ink-3 hover:text-ink"
                >
                  <span aria-hidden="true">✕</span>
                </button>
              ) : null}
            </div>
          );
        })}
      </div>

      {closed ? null : (
        <button
          type="button"
          onClick={onCreate}
          disabled={full}
          aria-label={t.newTab}
          title={full ? dict.errors.workspace_tab_limit : t.newTab}
          aria-describedby={full ? limitId : undefined}
          className="shrink-0 px-2 py-1 text-control-sm text-ink-2 hover:text-ink disabled:cursor-not-allowed disabled:text-ink-3"
        >
          <span aria-hidden="true">+</span>
        </button>
      )}
      {/* Why the + cannot be pressed. A disabled control announces nothing of
          its own, and the limit is the one thing a participant reaching for
          an eleventh tab needs to be told. */}
      {full ? (
        <p id={limitId} className="sr-only">
          {dict.errors.workspace_tab_limit}
        </p>
      ) : null}
      {status}
    </div>
  );
}


/**
 * What is happening to the open tab's text, at the end of the strip.
 *
 * Two lines, the same way the notes panel carries two (see its own doc): the
 * visible one follows every change, "Saving…" included; the screen reader's
 * live region carries only settled outcomes, so a save after every pause in
 * typing is not read out while a failure, the recovery from it and the
 * contest closing are.
 */
export function SqlTabStatus({
  status,
  error,
  stored,
  dict,
}: {
  /** The open tab's autosave status, or null when nothing here is saved. */
  status: AutosaveStatus | null;
  /** The code of the last refused action on the strip itself. */
  error: string | null;
  /** Whether these tabs are on the server at all. */
  stored: boolean;
  dict: PlayDictionary;
}) {
  const message = messageFor(status, error, stored, dict);
  const settledNow = status === null || (status.kind !== "saving" && status.kind !== "pending");
  const [settled, setSettled] = useState(settledNow ? message : "");
  if (settledNow && settled !== message) setSettled(message);

  return (
    <>
      <p
        data-testid="sql-tabs-status"
        aria-hidden="true"
        className={cn("shrink-0 truncate text-small", toneOf(status, error, stored))}
      >
        {message}
      </p>
      <p role="status" className="sr-only">
        {settled}
      </p>
    </>
  );
}

function messageFor(
  status: AutosaveStatus | null,
  error: string | null,
  stored: boolean,
  dict: PlayDictionary,
): string {
  const t = dict.participant.play.workspace.editor;
  const errors = dict.errors as Record<string, string>;
  // The workspace was never read, so there is nothing to save to and no
  // status to report — only the fact itself, which the participant has to
  // know before they type two hours of work into it.
  if (!stored) return t.unsaved;
  if (error !== null) return errors[error] ?? dict.errors.fallback;
  switch (status?.kind) {
    case undefined:
    case "saved":
      return t.status.saved;
    case "pending":
    case "saving":
      return t.status.saving;
    case "retrying":
      return t.status.retrying;
    case "closed":
      return t.status.closed;
    case "rejected":
      return errors[status.code] ?? dict.errors.fallback;
  }
}

function toneOf(status: AutosaveStatus | null, error: string | null, stored: boolean): string {
  if (!stored) return "text-warn";
  if (error !== null) return "text-bad";
  switch (status?.kind) {
    case "rejected":
    case "retrying":
      return "text-bad";
    case "closed":
      return "text-ink";
    default:
      return "text-ink-3";
  }
}
