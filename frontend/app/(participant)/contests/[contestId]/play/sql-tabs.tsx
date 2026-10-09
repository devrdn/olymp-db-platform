"use client";

import { useId, useRef, useState, useSyncExternalStore } from "react";

import { MAX_TABS, TAB_TITLE_MAX_CHARS } from "@/lib/api/workspace";
import { cn } from "@/lib/utils";

import type { PlayDictionary } from "./dictionary";
import type { AutosaveEngine, AutosaveStatus } from "./use-autosave";
import { messageForCode } from "@/lib/i18n/errors";

/** One tab, as the strip needs it. */
export type SqlTabView = { id: string; title: string };

/**
 * The strip above the SQL editor (SPEC.md §5): a tab per
 * document, a ✕ on each, a + at the end. It draws and reports and decides
 * nothing; `use-sql-tabs.ts` owns the texts and the server.
 *
 * Keyboard: the `tablist` pattern (arrows, Home, End, one tab stop), plus F2
 * to rename and Ctrl/⌘+Shift+Arrow to move a tab without a pointer.
 *
 * `closed` means the contest has ended for this participant: every write
 * would be refused, so no +, ✕, rename or drag is offered. Switching and
 * reading stay.
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
  /** Prefix for each tab's DOM id, so the editor panel can name its tab. */
  idPrefix: string;
  /** The editor's element, which the tabs control. */
  panelId: string;
  /** The contest is over: the strip is read-only. */
  closed: boolean;
  /** The active tab's save status, at the end of the strip. */
  status?: React.ReactNode;
  dict: PlayDictionary;
  onSelect: (id: string) => void;
  onCreate: () => void;
  /** A new name for a tab. The caller reports refusals: the server has the last word on a title. */
  onRename: (id: string, title: string) => void;
  onClose: (id: string) => void;
  /** Move tab `id` to index `to`. */
  onMove: (id: string, to: number) => void;
}) {
  const t = dict.participant.play.workspace.editor;
  const limitId = useId();
  const elements = useRef(new Map<string, HTMLElement | null>());
  const dragged = useRef<string | null>(null);
  const [renaming, setRenaming] = useState<string | null>(null);
  /** The tab a drag is hovering over, and which side of it the drop lands on. */
  const [dropAt, setDropAt] = useState<{ id: string; before: boolean } | null>(null);
  /** Set while Esc cancels a rename, so the resulting blur does not save it. */
  const cancelled = useRef(false);

  const full = tabs.length >= MAX_TABS;
  const closable = tabs.length > 1 && !closed;

  const select = (id: string) => {
    onSelect(id);
    // Focus follows selection, so the arrows work without waiting for the
    // parent's re-render.
    elements.current.get(id)?.focus();
  };

  const startRename = (id: string) => {
    if (!closed) setRenaming(id);
  };

  const finishRename = (tab: SqlTabView, value: string) => {
    setRenaming(null);
    // An unchanged name is not sent: every write counts against the
    // per-minute budget, refused or not.
    if (value !== tab.title) onRename(tab.id, value);
  };

  /**
   * Which half of a tab the pointer is over, which decides whether the drop
   * lands before or after it.
   */
  const onLeftHalf = (event: React.DragEvent<HTMLElement>): boolean => {
    const box = event.currentTarget.getBoundingClientRect();
    return box.width > 0 && event.clientX < box.left + box.width / 2;
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
              // The name is the title alone; the ✕ has its own label, and without
              // this the tab would read "Suspects, Close Suspects".
              aria-label={tab.title}
              aria-selected={active}
              aria-controls={panelId}
              tabIndex={active ? 0 : -1}
              draggable={!closed && renaming !== tab.id}
              onDragStart={(event) => {
                dragged.current = tab.id;
                // Firefox cancels a drag whose `DataTransfer` is empty and Safari is
                // unreliable, so the id is written onto it; `effectAllowed` and
                // `dropEffect` make the pointer say "move". Typed as optional
                // because jsdom implements neither `DragEvent` nor `DataTransfer`.
                const carried = event.dataTransfer as DataTransfer | undefined;
                carried?.setData("text/plain", tab.id);
                if (carried) carried.effectAllowed = "move";
              }}
              onDragOver={(event) => {
                if (dragged.current === null || dragged.current === tab.id) return;
                event.preventDefault();
                const carried = event.dataTransfer as DataTransfer | undefined;
                if (carried) carried.dropEffect = "move";
                const before = onLeftHalf(event);
                setDropAt((previous) =>
                  previous?.id === tab.id && previous.before === before
                    ? previous
                    : { id: tab.id, before },
                );
              }}
              onDrop={(event) => {
                event.preventDefault();
                const from = dragged.current;
                dragged.current = null;
                setDropAt(null);
                if (from === null || from === tab.id) return;
                onMove(from, landingIndex(tabs, from, index, onLeftHalf(event)));
              }}
              onDragEnd={() => {
                dragged.current = null;
                setDropAt(null);
              }}
              data-drop={dropAt?.id === tab.id ? (dropAt.before ? "before" : "after") : undefined}
              onClick={() => onSelect(tab.id)}
              onDoubleClick={() => startRename(tab.id)}
              onKeyDown={(event) => onTabKeyDown(event, tab, index)}
              className={cn(
                "relative flex min-w-0 shrink-0 cursor-pointer items-center gap-1.5 border-r border-line px-3 py-1.5",
                "text-control-sm transition-colors duration-(--t-input) ease-standard",
                // No focus style here: globals.css gives every focusable element
                // the same `:focus-visible` ring.
                active ? "bg-sunk text-ink" : "text-ink-2 hover:text-ink",
              )}
            >
              {renaming === tab.id ? (
                <input
                  autoFocus
                  // Selects the whole name when rename opens (F2 and double-click
                  // both mount this focused), so typing replaces it.
                  onFocus={(event) => event.currentTarget.select()}
                  aria-label={t.rename.replace("{tab}", tab.title)}
                  defaultValue={tab.title}
                  // The server's bound on a title. An empty or pasted-over name is
                  // still the server's to refuse.
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
              {dropAt?.id === tab.id ? (
                // Where the dragged tab will land.
                <span
                  aria-hidden="true"
                  className={cn("absolute inset-y-0 w-0.5 bg-accent", dropAt.before ? "left-0" : "right-0")}
                />
              ) : null}
              {closable ? (
                <button
                  type="button"
                  // Not a tab stop: the strip is one stop, and Delete on the focused
                  // tab reaches this button.
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
        // The tooltip sits on the span while the button is disabled: a
        // disabled control gets no pointer events, so its `title` never shows.
        // Screen readers get the described-by text below.
        <span
          title={full ? dict.errors.workspace_tab_limit : undefined}
          className="flex shrink-0 items-center"
        >
          <button
            type="button"
            onClick={onCreate}
            disabled={full}
            aria-label={t.newTab}
            title={full ? undefined : t.newTab}
            aria-describedby={full ? limitId : undefined}
            className="px-2 py-1 text-control-sm text-ink-2 hover:text-ink disabled:cursor-not-allowed disabled:text-ink-3"
          >
            <span aria-hidden="true">+</span>
          </button>
        </span>
      )}
      {/* Why the + is disabled: a disabled control announces nothing itself. */}
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
 * The open tab's save status, at the end of the strip. As in the notes
 * panel, the visible line follows every change while the live region carries
 * only settled outcomes, so a save after each pause is not read aloud.
 */
export function SqlTabStatus({
  engine,
  error,
  stored,
  dict,
}: {
  /**
   * The engine saving the open tab, or null when nothing is saved.
   * Subscribed here so a save re-renders this line and not the editor.
   */
  engine: AutosaveEngine | null;
  /** The code of the strip's last refused action. */
  error: string | null;
  /** Whether these tabs are on the server at all. */
  stored: boolean;
  dict: PlayDictionary;
}) {
  const status = useSyncExternalStore(
    engine?.subscribe ?? noSubscribe,
    engine?.getStatus ?? noStatus,
    noStatus,
  );
  const message = messageFor(status, error, stored, dict);
  const settledNow = status === null || (status.kind !== "saving" && status.kind !== "pending");
  const [settled, setSettled] = useState(settledNow ? message : "");
  if (settledNow && settled !== message) setSettled(message);

  return (
    <>
      <p
        data-testid="sql-tabs-status"
        aria-hidden="true"
        title={message}
        // Bounded and shrinkable, so a long sentence gives way to the tabs; the
        // full text is in the title and the live region.
        className={cn("min-w-0 max-w-48 truncate text-small", toneOf(status, error, stored))}
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
  // The workspace was never read: nothing is saved, and the participant
  // must know before typing into it.
  if (!stored) return t.unsaved;
  if (error !== null) return messageForCode(error, dict.errors);
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
      return messageForCode(status.code, dict.errors);
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

/** For a tab with no engine: the local tab of a workspace that failed to load. */
function noSubscribe() {
  return () => {};
}

function noStatus(): AutosaveStatus | null {
  return null;
}

/**
 * The index a tab dropped on the tab at `targetIndex` lands at. A tab dragged
 * rightwards leaves its place first, shifting the later tabs left by one, so
 * the target index is corrected for that.
 */
export function landingIndex(
  tabs: readonly SqlTabView[],
  draggedId: string,
  targetIndex: number,
  before: boolean,
): number {
  const from = tabs.findIndex((tab) => tab.id === draggedId);
  const target = targetIndex - (from < targetIndex ? 1 : 0);
  return target + (before ? 0 : 1);
}
