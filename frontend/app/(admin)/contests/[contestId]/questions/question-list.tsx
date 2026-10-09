"use client";

import Link from "next/link";
import { useActionState } from "react";

import { StateView } from "@/components/product/state-view";
import { Button } from "@/components/ui/button";
import { Tag } from "@/components/ui/tag";
import { Tooltip } from "@/components/ui/tooltip";
import { answerable, untranslated } from "@/lib/api/content-terms";
import { type Question } from "@/lib/api/content";
import type { Scoring } from "@/lib/api/contests";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import {
  addQuestionAction,
  deleteQuestionAction,
  reorderQuestionsAction,
  type QuestionListState,
} from "./actions";
import { messageForCode } from "@/lib/i18n/errors";

const HEAD =
  "border-b border-line-2 px-(--row-px) py-2.5 font-mono text-label font-medium text-ink-3 uppercase";
const CELL = "border-b border-line px-(--row-px) py-(--row-py) align-top";

/**
 * The question list in contest order, with move controls rather than
 * drag-and-drop so it works from a keyboard. Each move submits the complete new
 * order; the repository swaps inside a transaction with a deferred constraint,
 * since both rows briefly share a position.
 */
export function QuestionList({
  contestId,
  questions,
  languages,
  editable,
  scoring,
  dict,
}: {
  contestId: string;
  questions: Question[];
  languages: string[];
  editable: boolean;
  // ICPC has no per-question points, so the column is hidden.
  scoring: Scoring;
  dict: Dictionary;
}) {
  const t = dict.workspace.questions;
  const showPoints = scoring !== "icpc";

  if (questions.length === 0) {
    return (
      <div className="flex flex-col gap-8 border-t border-line">
        {/* This list has no filters to clear. */}
        <StateView state={{ kind: "empty", title: t.empty.title, body: t.empty.body }} />
        {editable ? <AddQuestion contestId={contestId} dict={dict} /> : null}
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-8">
      <div className="overflow-x-auto">
        <table className="w-full min-w-2xl border-collapse text-left">
          <thead>
            <tr>
              <th scope="col" className={cn(HEAD, "w-10 pr-0 text-right")}>
                {t.columns.ord}
              </th>
              <th scope="col" className={HEAD}>
                {t.columns.question}
              </th>
              <th scope="col" className={cn(HEAD, "w-36")}>
                {t.columns.kind}
              </th>
              {showPoints ? (
                <th scope="col" className={cn(HEAD, "w-20 text-right")}>
                  {t.columns.points}
                </th>
              ) : null}
              <th scope="col" className={cn(HEAD, "w-56")}>
                {t.columns.state}
              </th>
              <th scope="col" className={cn(HEAD, "w-32")}>
                <span className="sr-only">{t.moveUp}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {questions.map((question, index) => {
              const missing = untranslated(question, languages);

              return (
                <tr key={question.id} className="transition-colors duration-(--t-input) ease-standard hover:bg-panel">
                  {/* The contest's numbering, not the array index, so this and
                     the question screen agree. */}
                  <td className={cn(CELL, "pr-0 text-right font-mono text-data text-ink-3")}>
                    {String(question.ord).padStart(2, "0")}
                  </td>

                  <td className={CELL}>
                    <Link
                      href={`/contests/${contestId}/questions/${question.id}`}
                      className="block w-fit max-w-body text-row text-ink underline decoration-edge underline-offset-4 transition-colors duration-(--t-input) ease-standard hover:decoration-ink"
                    >
                      {/* The body's first line in any language; a draft without
                         one is still openable. */}
                      {firstLine(question) || (
                        <span className="text-ink-3">{t.untitled}</span>
                      )}
                    </Link>
                  </td>

                  <td className={cn(CELL, "text-small text-ink-2")}>{t.kind[question.kind]}</td>

                  {showPoints ? (
                    <td className={cn(CELL, "text-right font-mono text-data text-ink-2")}>
                      {question.points}
                    </td>
                  ) : null}

                  <td className={CELL}>
                    <div className="flex flex-wrap gap-1.5">
                      {/* Only what is wrong or unusual is marked. */}
                      {/* A Tooltip beside the tag, since `title` is unreachable
                         by touch or keyboard. */}
                      {!question.isVisible ? (
                        <span className="flex items-center gap-1">
                          <Tag tone="mute">{t.hidden}</Tag>
                          <Tooltip label={dict.chrome.helpLabel}>{t.hiddenHint}</Tooltip>
                        </span>
                      ) : null}
                      {!answerable(question) ? <Tag tone="warn">{t.noAnswer}</Tag> : null}
                      {missing.length > 0 ? (
                        <Tag tone="warn">{t.missingText.replace("{n}", String(missing.length))}</Tag>
                      ) : null}
                    </div>
                  </td>

                  <td className={cn(CELL, "text-right")}>
                    {editable ? (
                      <RowControls
                        contestId={contestId}
                        questions={questions}
                        index={index}
                        dict={dict}
                      />
                    ) : null}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      {editable ? <AddQuestion contestId={contestId} dict={dict} /> : null}
    </div>
  );
}

function firstLine(question: Question): string {
  for (const text of Object.values(question.texts)) {
    const line = text.bodyMd.trim().split("\n")[0];
    if (line) return line.length > 120 ? `${line.slice(0, 120)}…` : line;
  }
  return "";
}

/**
 * Move and delete controls. Separate forms, so the delete's confirmation and
 * pending state do not affect the moves.
 */
function RowControls({
  contestId,
  questions,
  index,
  dict,
}: {
  contestId: string;
  questions: Question[];
  index: number;
  dict: Dictionary;
}) {
  const t = dict.workspace.questions;

  const swapped = (a: number, b: number) => {
    const order = questions.map((q) => q.id);
    [order[a], order[b]] = [order[b], order[a]];
    return order;
  };

  return (
    <div className="flex items-center justify-end gap-1">
      {index > 0 ? (
        <ReorderButton
          contestId={contestId}
          order={swapped(index, index - 1)}
          label={t.moveUp}
          glyph="↑"
        />
      ) : (
        <span className="inline-block size-7" />
      )}

      {index < questions.length - 1 ? (
        <ReorderButton
          contestId={contestId}
          order={swapped(index, index + 1)}
          label={t.moveDown}
          glyph="↓"
        />
      ) : (
        <span className="inline-block size-7" />
      )}

      <DeleteQuestion contestId={contestId} questionId={questions[index].id} dict={dict} />
    </div>
  );
}

function ReorderButton({
  contestId,
  order,
  label,
  glyph,
}: {
  contestId: string;
  order: string[];
  label: string;
  glyph: string;
}) {
  const [, formAction, pending] = useActionState<QuestionListState, FormData>(
    reorderQuestionsAction,
    {},
  );

  return (
    <form action={formAction} className="contents">
      <input type="hidden" name="contestId" value={contestId} />
      {order.map((id, position) => (
        <input key={`${id}-${position}`} type="hidden" name="order" value={id} />
      ))}

      <Button type="submit" size="icon" variant="quiet" disabled={pending} aria-label={label}>
        <span aria-hidden>{glyph}</span>
      </Button>
    </form>
  );
}

function DeleteQuestion({
  contestId,
  questionId,
  dict,
}: {
  contestId: string;
  questionId: string;
  dict: Dictionary;
}) {
  const t = dict.workspace.questions;
  const [, formAction, pending] = useActionState<QuestionListState, FormData>(
    deleteQuestionAction,
    {},
  );

  return (
    <form
      action={formAction}
      className="contents"
      /* Deletion is permanent. A native confirm also works before hydration. */
      onSubmit={(event) => {
        if (!window.confirm(t.confirmRemove)) event.preventDefault();
      }}
    >
      <input type="hidden" name="contestId" value={contestId} />
      <input type="hidden" name="questionId" value={questionId} />

      <Button type="submit" size="icon" variant="quiet" disabled={pending} aria-label={t.remove}>
        <span aria-hidden className="text-bad">
          ×
        </span>
      </Button>
    </form>
  );
}

function AddQuestion({ contestId, dict }: { contestId: string; dict: Dictionary }) {
  const t = dict.workspace.questions;
  const [state, formAction, pending] = useActionState<QuestionListState, FormData>(
    addQuestionAction,
    {},
  );

  const failure = state.code
    ? (messageForCode(state.code, dict.errors))
    : null;

  return (
    <form action={formAction} className="flex flex-col gap-3">
      <input type="hidden" name="contestId" value={contestId} />

      <Button type="submit" disabled={pending} className="self-start">
        {pending ? t.adding : t.add}
      </Button>

      {failure ? (
        <p role="alert" className="max-w-body text-small text-bad">
          {failure}
        </p>
      ) : null}
    </form>
  );
}
