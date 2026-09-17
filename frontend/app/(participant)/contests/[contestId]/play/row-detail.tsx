"use client";

import { useCallback, useState } from "react";

import { Button } from "@/components/ui/button";
import { toCsv } from "@/lib/format/csv";
import { cn } from "@/lib/utils";

import type { PlayDictionary } from "./dictionary";

/**
 * One row of a query's result, open in full — §7 of the workspace design.
 *
 * The table above it has to clip: every row is the same height and every
 * column a declared width, which is what makes the window over a
 * thousand-row answer arithmetic rather than measurement (see
 * `result-panel.tsx`). The price of that is a cell a participant cannot read,
 * and a `title` attribute is not a way to read a witness statement. So the
 * row opens here instead: every column of it, every value whole, wrapped
 * rather than clipped, and each one takeable out of the page.
 *
 * Only the result table opens a row. The query log deliberately does not
 * (the plan's own words): what a log entry holds is one query's text, which
 * has its own place, not a row of somebody's data.
 */

/**
 * Puts text on the clipboard, or says it could not.
 *
 * Two ways, because the first one is not always there: `navigator.clipboard`
 * is a secure-context API, and a classroom machine reaching this service over
 * plain HTTP on the local network — which is exactly how an on-premise
 * olympiad is often run — has no `clipboard` at all. The old
 * `document.execCommand("copy")` over a detached textarea still works
 * everywhere, so it is what the failure falls through to.
 *
 * Nothing here throws. A refused clipboard is a line under the buttons, not
 * an exception that takes the result panel down with the copy.
 */
async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // Permission refused, or a clipboard the browser will not hand over
    // without a gesture it did not see. The selection below may still work.
  }

  // Selecting a textarea takes the focus with it, and the focus is what the
  // row's own Esc and arrows are attached to — left on `<body>`, the panel
  // stops answering the keyboard on exactly the plain-HTTP machines this
  // branch exists for. So: remember what had it, and give it back.
  const focused = document.activeElement as HTMLElement | null;
  const area = document.createElement("textarea");
  try {
    area.value = text;
    area.setAttribute("readonly", "");
    // Off-screen rather than hidden: a `display: none` textarea has no
    // selection to copy, and a fixed one does not scroll the page under the
    // participant on the way.
    area.style.position = "fixed";
    area.style.top = "0";
    area.style.opacity = "0";
    document.body.appendChild(area);
    // Focused as well as selected: some browsers copy nothing from a
    // selection in an unfocused field, and doing it explicitly is also what
    // makes the focus this steals something the `finally` below can give back.
    area.focus({ preventScroll: true });
    area.select();
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    area.remove();
    if (focused?.isConnected) focused.focus({ preventScroll: true });
  }
}

/** What the last copy did, or nothing yet. */
type Notice = "copied" | "failed" | null;

export function RowDetail({
  columns,
  columnTypes,
  row,
  index,
  nullLabel,
  dict,
  onClose,
  onKeyDown,
  className,
}: {
  columns: readonly string[];
  columnTypes?: readonly string[];
  row: readonly (string | null)[];
  /** Which row of the whole answer this is, counted from zero as the table counts. */
  index: number;
  /** The word the table prints for a null, so the two never disagree. */
  nullLabel: string;
  dict: PlayDictionary;
  onClose: () => void;
  /** The arrows and Esc, handled by whoever owns the selection. */
  onKeyDown?: (event: React.KeyboardEvent) => void;
  /** How the pane above sizes this — see the result split's own comment. */
  className?: string;
}) {
  const t = dict.participant.play.workspace.row;
  const [notice, setNotice] = useState<Notice>(null);

  // A notice belongs to the row it was shown on: arrowing to the next row
  // must not leave "Copied" standing over a value nobody copied. Cleared
  // during the render that brings the new row in rather than in an effect
  // afterwards — React's own "reset state when a prop changes" pattern, and
  // the one the result table beside it already uses for its own window.
  const [shown, setShown] = useState(row);
  if (shown !== row) {
    setShown(row);
    setNotice(null);
  }

  const copy = useCallback(async (text: string) => {
    setNotice((await copyText(text)) ? "copied" : "failed");
  }, []);

  const number = String(index + 1);

  return (
    <div
      role="region"
      aria-label={t.region.replace("{n}", number)}
      onKeyDown={onKeyDown}
      className={cn("flex min-h-0 flex-col overflow-hidden", className)}
    >
      <div className="flex shrink-0 items-center gap-2 border-b border-line px-3 py-1.5">
        <span className="font-mono text-label text-ink-3 uppercase">
          {t.heading.replace("{n}", number)}
        </span>
        <div className="flex-1" />
        {/* The whole row in the same shape the table's own download has, so
            what is pasted into a spreadsheet lands in the same columns. */}
        <Button type="button" variant="quiet" size="sm" onClick={() => copy(toCsv(columns, [row]))}>
          {t.copyRow}
        </Button>
        <Button type="button" variant="quiet" size="sm" onClick={onClose}>
          {t.close}
        </Button>
      </div>

      {/* On the page from the start, empty. A live region only announces what
          changes *inside* it: one created together with its own text is a
          region the reader never had, and a refused copy — the one message
          that matters — would go unsaid. Empty it draws no line box, so it
          costs nothing until there is something to say. */}
      <p
        role="status"
        className={cn(
          "shrink-0 px-3 text-small",
          notice === null ? null : "py-1",
          notice === "failed" ? "text-warn" : "text-ink-3",
        )}
      >
        {notice === null ? "" : notice === "copied" ? t.copied : t.copyFailed}
      </p>

      <dl className="min-h-0 flex-1 overflow-auto px-3 py-1 text-body">
        {columns.map((column, c) => {
          const value = row[c] ?? null;
          return (
            <div
              key={`${column}-${c}`}
              className="grid grid-cols-[minmax(0,12rem)_minmax(0,1fr)_auto] items-baseline gap-3 border-b border-edge py-1.5 last:border-b-0 max-narrow:grid-cols-1"
            >
              <dt className="truncate font-mono text-label text-ink-3 uppercase" title={column}>
                {column}
                {/* The type under the name, as the table's own header draws
                    it — and absent rather than wrong when this build could
                    not name it. */}
                {columnTypes?.[c] ? (
                  <span className="block truncate font-normal normal-case">{columnTypes[c]}</span>
                ) : null}
              </dt>
              <dd className="min-w-0 font-mono whitespace-pre-wrap break-words text-ink">
                {value === null ? (
                  <span className="text-ink-2 italic">{nullLabel}</span>
                ) : (
                  value
                )}
              </dd>
              {/* A null has no text to take: the word on screen is this
                  interface's, not the database's, and pasting it would be
                  pasting a translation. */}
              {value === null ? (
                <span />
              ) : (
                <Button
                  type="button"
                  variant="quiet"
                  size="sm"
                  // The visible words say what it does; the accessible name
                  // says which of the columns it does it to, because a
                  // fifteen-column row is fifteen buttons that otherwise read
                  // the same.
                  aria-label={t.copyValueNamed.replace("{column}", column)}
                  onClick={() => copy(value)}
                >
                  {t.copyValue}
                </Button>
              )}
            </div>
          );
        })}
      </dl>
    </div>
  );
}
