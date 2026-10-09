"use client";

import { useCallback, useId, useRef, useState } from "react";

import {
  NOTES_COUNTER_FROM,
  NOTES_MAX_CHARS,
  saveNotes,
  type WorkspaceNotes,
} from "@/lib/api/workspace";

import type { PlayDictionary } from "./dictionary";
import { useAutosave, type AutosaveStatus } from "./use-autosave";
import { messageForCode } from "@/lib/i18n/errors";

/**
 * The notes tab (docs/ARCHITECTURE.md §6.4): one plain, self-saving field
 * with its save status and, near the limit, a character counter.
 *
 * The field is uncontrolled: typing re-renders nothing unless the status
 * changes or the counter is showing. `initial` is null when the workspace
 * could not be read, and then no field is offered, since typing into an
 * empty one would save over notes the server still holds.
 *
 * The visible status follows every change; the live region carries only
 * settled outcomes, so routine saves are not read aloud.
 */
export function NotesPanel({
  accountId,
  contestId,
  initial,
  dict,
}: {
  /** Whose notes these are; the draft is keyed by it. */
  accountId: string | null;
  contestId: string;
  initial: WorkspaceNotes | null;
  dict: PlayDictionary;
}) {
  const t = dict.participant.play.workspace.notes;

  if (initial === null) {
    return <p className="p-4 text-body text-ink-2">{t.failed}</p>;
  }
  return <NotesEditor accountId={accountId} contestId={contestId} initial={initial} dict={dict} />;
}

function NotesEditor({
  accountId,
  contestId,
  initial,
  dict,
}: {
  accountId: string | null;
  contestId: string;
  initial: WorkspaceNotes;
  dict: PlayDictionary;
}) {
  const t = dict.participant.play.workspace.notes;
  const fieldRef = useRef<HTMLTextAreaElement>(null);
  const fieldId = useId();
  const statusId = useId();
  const counterId = useId();

  // Null below the threshold, so an edit there sets the same value and
  // React skips the render.
  const [count, setCount] = useState<number | null>(() => countFor(initial.body));

  const save = useCallback(
    (text: string, options: { keepalive: boolean }) =>
      saveNotes(contestId, text, options).then((answer) => answer.updatedAt),
    [contestId],
  );
  const onRestore = useCallback((text: string) => {
    if (fieldRef.current) fieldRef.current.value = text;
    setCount(countFor(text));
  }, []);

  const { status, setValue, flush } = useAutosave({
    accountId,
    contestId,
    documentKey: "notes",
    initialText: initial.body,
    initialVersion: initial.updatedAt,
    save,
    onRestore,
  });

  // The last settled outcome, for the live region; "saving" and "pending"
  // do not replace it.
  const [settled, setSettled] = useState<AutosaveStatus>(status);
  if (status.kind !== "saving" && status.kind !== "pending" && status !== settled) {
    setSettled(status);
  }

  const full = count !== null && count >= NOTES_MAX_CHARS;
  const describedBy = count !== null ? `${statusId} ${counterId}` : statusId;

  return (
    <div className="flex h-full min-h-0 flex-1 flex-col gap-2 p-4">
      <label htmlFor={fieldId} className="sr-only">
        {t.label}
      </label>
      <textarea
        ref={fieldRef}
        id={fieldId}
        defaultValue={initial.body}
        maxLength={NOTES_MAX_CHARS}
        // Not read-only when the contest ends, as with the SQL editor:
        // saving stops, the draft stays, and the status line says why.
        placeholder={t.placeholder}
        spellCheck={false}
        aria-describedby={describedBy}
        // Pastes here are reported to the organiser (use-signals.ts).
        data-paste-target="notes"
        onChange={(event) => {
          const text = event.currentTarget.value;
          setValue(text);
          setCount(countFor(text));
        }}
        onBlur={flush}
        className={
          "min-h-40 w-full flex-1 resize-none rounded-none border border-edge bg-transparent px-3 py-2.5 " +
          "font-sans text-body text-ink placeholder:text-ink-3 " +
          "transition-colors duration-(--t-input) ease-standard " +
          "hover:border-ink-2 focus-visible:border-ink focus-visible:outline-none"
        }
      />
      {/* The organiser sees the notes and their history (docs/ARCHITECTURE.md §6.4). */}
      <p className="text-small text-ink-2">{t.observed}</p>
      <div className="flex items-start justify-between gap-3">
        <p data-testid="notes-status" aria-hidden="true" className={`text-small ${toneOf(status)}`}>
          {messageFor(status, dict)}
        </p>
        <p id={statusId} role="status" className="sr-only">
          {messageFor(settled, dict)}
        </p>
        {count !== null ? (
          <p
            id={counterId}
            data-testid="notes-counter"
            className={`shrink-0 text-small tabular-nums ${full ? "text-bad" : "text-ink-2"}`}
          >
            {t.counter.replace("{n}", String(count)).replace("{max}", String(NOTES_MAX_CHARS))}
          </p>
        ) : null}
      </div>
      {/* At the limit the field silently stops taking text, so a screen reader
          is told once, in a region apart from the save status. */}
      <p data-testid="notes-limit" aria-live="polite" className="sr-only">
        {full ? t.limitReached : ""}
      </p>
    </div>
  );
}

/**
 * The counter value, or null below the threshold. Plain digits without
 * locale grouping: server and browser ICU data may group differently, which
 * would be a hydration mismatch.
 */
function countFor(text: string): number | null {
  // UTF-16 code units, as `maxLength` counts them: never fewer than the
  // server's character count, so the field stops before the server would
  // refuse.
  return text.length >= NOTES_COUNTER_FROM ? text.length : null;
}

function messageFor(status: AutosaveStatus, dict: PlayDictionary): string {
  const t = dict.participant.play.workspace.notes.status;
  switch (status.kind) {
    case "saved":
      return t.saved;
    case "pending":
    case "saving":
      return t.saving;
    case "retrying":
      return t.retrying;
    case "closed":
      return t.closed;
    case "rejected": {
      return messageForCode(status.code, dict.errors);
    }
  }
}

function toneOf(status: AutosaveStatus): string {
  switch (status.kind) {
    case "rejected":
    case "retrying":
      return "text-bad";
    case "closed":
      return "text-ink";
    default:
      return "text-ink-2";
  }
}
