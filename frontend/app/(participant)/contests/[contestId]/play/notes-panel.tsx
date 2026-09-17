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

/**
 * The notes tab beside the console (§6 of the workspace design): one plain
 * field, no Markdown, that saves itself, with the save's status under it
 * and a character counter once the notes near the limit.
 *
 * The field is uncontrolled. Typing hands the text to the autosave engine
 * and re-renders nothing, unless the status changes or the notes are within
 * reach of the limit, where the counter has to follow every keystroke. This
 * component owns all of that state, so the tabs beside it never re-render
 * for it.
 *
 * `initial` is null when the page could not read the workspace. The field is
 * then not offered at all: whatever was typed into an empty field would be
 * saved over notes the server still holds.
 *
 * Two status lines, on purpose. The visible one follows every change,
 * "Saving…" included; the screen reader's live region carries only settled
 * outcomes, so a routine save after each pause in typing is not read out,
 * while a failure, the recovery from it and the contest closing are.
 */
export function NotesPanel({
  contestId,
  initial,
  dict,
  locale,
}: {
  contestId: string;
  initial: WorkspaceNotes | null;
  dict: PlayDictionary;
  locale: string;
}) {
  const t = dict.participant.play.workspace.notes;

  if (initial === null) {
    return <p className="p-4 text-body text-ink-2">{t.failed}</p>;
  }
  return <NotesEditor contestId={contestId} initial={initial} dict={dict} locale={locale} />;
}

function NotesEditor({
  contestId,
  initial,
  dict,
  locale,
}: {
  contestId: string;
  initial: WorkspaceNotes;
  dict: PlayDictionary;
  locale: string;
}) {
  const t = dict.participant.play.workspace.notes;
  const fieldRef = useRef<HTMLTextAreaElement>(null);
  const fieldId = useId();
  const statusId = useId();
  const counterId = useId();

  // Null below the threshold, so an edit there sets the same value again and
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
    contestId,
    documentKey: "notes",
    initialText: initial.body,
    initialVersion: initial.updatedAt,
    save,
    onRestore,
  });

  // The last settled outcome, for the live region: "saving" and "pending"
  // are passing states and do not replace it. Adjusted during render, the
  // way React recommends for state derived from a changing input.
  const [settled, setSettled] = useState<AutosaveStatus>(status);
  if (status.kind !== "saving" && status.kind !== "pending" && status !== settled) {
    setSettled(status);
  }

  const closed = status.kind === "closed";
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
        readOnly={closed}
        placeholder={t.placeholder}
        spellCheck={false}
        aria-describedby={describedBy}
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
          "hover:border-ink-2 focus-visible:border-ink focus-visible:outline-none " +
          "read-only:bg-sunk read-only:text-ink-2"
        }
      />
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
            className={`shrink-0 text-small tabular-nums ${count >= NOTES_MAX_CHARS ? "text-bad" : "text-ink-2"}`}
          >
            {t.counter
              .replace("{n}", formatCount(count, locale))
              .replace("{max}", formatCount(NOTES_MAX_CHARS, locale))}
          </p>
        ) : null}
      </div>
    </div>
  );
}

function countFor(text: string): number | null {
  // UTF-16 code units, the same measure `maxLength` enforces; never more
  // characters than the server counts, so the field stops before it refuses.
  return text.length >= NOTES_COUNTER_FROM ? text.length : null;
}

function formatCount(n: number, locale: string): string {
  return new Intl.NumberFormat(locale).format(n);
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
      const errors = dict.errors as Record<string, string>;
      return errors[status.code] ?? dict.errors.fallback;
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
