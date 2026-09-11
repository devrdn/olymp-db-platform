"use client";

import Link from "next/link";
import { useActionState } from "react";

import { StateView } from "@/components/product/state-view";
import { Button } from "@/components/ui/button";
import { Tag } from "@/components/ui/tag";
import { Tooltip } from "@/components/ui/tooltip";
import { answerable, untranslated } from "@/lib/api/content-terms";
import { type Question } from "@/lib/api/content";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import {
  addQuestionAction,
  deleteQuestionAction,
  reorderQuestionsAction,
  type QuestionListState,
} from "./actions";

const HEAD =
  "border-b border-line-2 px-(--row-px) py-2.5 font-mono text-label font-medium text-ink-3 uppercase";
const CELL = "border-b border-line px-(--row-px) py-(--row-py) align-top";

/**
 * The question list, as a register in the contest's own order.
 *
 * The order is the thing being edited here, so it is the thing the screen is
 * built around: a numbered column, and a move on each row. Drag-and-drop was
 * the obvious alternative and it is the wrong one — the list is the one place
 * that has to work from a keyboard without exception, and a question moved by
 * mistake in a published contest is a question every participant meets in a
 * different place.
 *
 * Each move submits the complete new order rather than "swap 3 and 4", which
 * is what the endpoint takes: the repository performs the exchange inside a
 * transaction with a deferred constraint, because midway through it both rows
 * hold the same position.
 */
export function QuestionList({
  contestId,
  questions,
  languages,
  editable,
  dict,
}: {
  contestId: string;
  questions: Question[];
  languages: string[];
  editable: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.questions;

  if (questions.length === 0) {
    return (
      <div className="flex flex-col gap-8 border-t border-line">
        {/* `empty`, never `empty-filtered`: this list has no filters, so there
            is no control to clear and offering one would be a lie. */}
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
              <th scope="col" className={cn(HEAD, "w-20 text-right")}>
                {t.columns.points}
              </th>
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
                  {/* The contest's own numbering, not this row's position in
                      the array. The question's screen prints the same figure,
                      and two screens deriving it separately is how they come to
                      disagree about which question is open. */}
                  <td className={cn(CELL, "pr-0 text-right font-mono text-data text-ink-3")}>
                    {String(question.ord).padStart(2, "0")}
                  </td>

                  <td className={CELL}>
                    <Link
                      href={`/contests/${contestId}/questions/${question.id}`}
                      className="block w-fit max-w-body text-row text-ink underline decoration-edge underline-offset-4 transition-colors duration-(--t-input) ease-standard hover:decoration-ink"
                    >
                      {/* The first line of the body in any language it has.
                          A question is recognised by its words, and a draft
                          with none still needs to be openable. */}
                      {firstLine(question) || (
                        <span className="text-ink-3">{t.untitled}</span>
                      )}
                    </Link>
                  </td>

                  <td className={cn(CELL, "text-small text-ink-2")}>{t.kind[question.kind]}</td>

                  <td className={cn(CELL, "text-right font-mono text-data text-ink-2")}>
                    {question.points}
                  </td>

                  <td className={CELL}>
                    <div className="flex flex-wrap gap-1.5">
                      {/* Only what is wrong or unusual is marked. A row with no
                          tags is a question that is finished, and that is worth
                          being able to see down the column. */}
                      {/* `title` cannot be reached by touch or keyboard, so
                          the explanation sits beside the tag as the same
                          Tooltip every "?" affordance in the product uses,
                          rather than on the tag itself. */}
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

/** The body's opening line, in whichever language has one. */
function firstLine(question: Question): string {
  for (const text of Object.values(question.texts)) {
    const line = text.bodyMd.trim().split("\n")[0];
    if (line) return line.length > 120 ? `${line.slice(0, 120)}…` : line;
  }
  return "";
}

/**
 * Moving one question, and deleting it.
 *
 * Three forms rather than one with three buttons: the delete needs a
 * confirmation the moves must not inherit, and a shared pending flag would
 * grey out the move while the delete is in flight.
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
      /* A question and its reference answers do not come back. The confirm is
         native because it must also work before hydration, which is exactly
         when a stray click is most likely. */
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
    ? ((dict.errors as Record<string, string>)[state.code] ?? dict.errors.fallback)
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
