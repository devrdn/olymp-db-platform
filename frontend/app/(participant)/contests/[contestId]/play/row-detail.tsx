"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { toCsv } from "@/lib/format/csv";
import { cn } from "@/lib/utils";

import type { PlayDictionary } from "./dictionary";

/**
 * One row of a query's result, open in full (SPEC.md §5).
 * The table clips cells to keep its window arithmetic (`result-panel.tsx`);
 * here every value is whole, wrapped, and can be copied. Only the result
 * table opens rows; the query log does not.
 */

/**
 * Puts text on the clipboard and reports whether it could.
 * `navigator.clipboard` exists only in a secure context, and an on-premise
 * olympiad is often served over plain HTTP, so the fallback is
 * `execCommand("copy")` on a temporary textarea. Never throws.
 */
async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // Permission refused or no user gesture seen; the fallback may still work.
  }

  // Selecting the textarea takes the focus, which the row's Esc and arrows
  // depend on, so it is given back afterwards.
  const focused = document.activeElement as HTMLElement | null;
  const area = document.createElement("textarea");
  try {
    area.value = text;
    area.setAttribute("readonly", "");
    // Off-screen rather than hidden: a `display: none` textarea has no
    // selection, and `fixed` does not scroll the page.
    area.style.position = "fixed";
    area.style.top = "0";
    area.style.opacity = "0";
    document.body.appendChild(area);
    // Focused as well as selected: some browsers copy nothing from an
    // unfocused field.
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
  /** The row's zero-based index in the whole answer. */
  index: number;
  /** The word the table prints for a null, so the two agree. */
  nullLabel: string;
  dict: PlayDictionary;
  onClose: () => void;
  /** The arrows and Esc, handled by the selection's owner. */
  onKeyDown?: (event: React.KeyboardEvent) => void;
  /** Sizing from the result split (see its comment). */
  className?: string;
}) {
  const t = dict.participant.play.workspace.row;
  const [notice, setNotice] = useState<Notice>(null);
  const regionRef = useRef<HTMLDivElement>(null);

  /**
   * Takes the focus when the row opens (this mounts only then). Neither
   * `aria-selected` on a table row nor a new panel is announced, so the
   * focus move is the announcement, reading the region's name. Closing
   * returns the focus to the row (`SelectableRows.close`).
   */
  useEffect(() => {
    regionRef.current?.focus({ preventScroll: true });
  }, []);

  // A notice belongs to its row: arrowing on clears "Copied". Reset during
  // render, React's pattern for resetting state on a prop change.
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
      ref={regionRef}
      role="region"
      aria-label={t.region.replace("{n}", number)}
      // Focused by script only, never a tab stop.
      tabIndex={-1}
      onKeyDown={onKeyDown}
      className={cn("flex min-h-0 flex-col overflow-hidden", className)}
    >
      <div className="flex shrink-0 items-center gap-2 border-b border-line px-3 py-1.5">
        <span className="font-mono text-label text-ink-3 uppercase">
          {t.heading.replace("{n}", number)}
        </span>
        <div className="flex-1" />
        {/* CSV, like the table's download, so a paste lands in the same columns. */}
        <Button type="button" variant="quiet" size="sm" onClick={() => copy(toCsv(columns, [row]))}>
          {t.copyRow}
        </Button>
        <Button type="button" variant="quiet" size="sm" onClick={onClose}>
          {t.close}
        </Button>
      </div>

      {/* Rendered empty from the start: a live region announces only changes
          inside it, so one created with its text would say nothing. */}
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
                {/* The type under the name, absent when unknown. */}
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
              {/* A null has no text to copy; the word on screen is a translation. */}
              {value === null ? (
                <span />
              ) : (
                <Button
                  type="button"
                  variant="quiet"
                  size="sm"
                  // The accessible name names the column, or every copy button in a
                  // wide row would read the same.
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
