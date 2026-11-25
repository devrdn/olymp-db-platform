"use client";

import Link from "next/link";

import { ActionButton, ActionForm, ActionOutcome } from "@/components/product/action-form";
import { StateView } from "@/components/product/state-view";
import { ScrollBox } from "@/components/ui/scroll-box";
import { Tag } from "@/components/ui/tag";
import { Tooltip } from "@/components/ui/tooltip";
import { type Question } from "@/lib/api/content";
import { answerable, untranslated } from "@/lib/api/content-terms";
import type { Contest, Scoring } from "@/lib/api/contests";
import { atVersion, NO_DRAFT } from "@/lib/api/contests-terms";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { fill } from "@/lib/i18n/fill";
import { cn } from "@/lib/utils";

import { ReloadWhenChanged } from "../reload-when-changed";
import { addQuestionAction, deleteQuestionAction, reorderQuestionsAction } from "./actions";

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
  contest,
  questions,
  languages,
  editable,
  scoring,
  dict,
}: {
  contest: Pick<Contest, "id" | "version">;
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
        {editable ? <AddQuestion contest={contest} dict={dict} /> : null}
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-8">
      <ScrollBox>
        <table className="w-full min-w-3xl border-collapse text-left">
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
                      href={`/contests/${contest.id}/questions/${question.id}`}
                      className="block w-fit max-w-body text-row text-ink underline decoration-edge underline-offset-4 transition-colors duration-(--t-input) ease-standard hover:decoration-ink"
                    >
                      {/* The title, or else the body's first line, in any
                         language; a draft without either is still openable. */}
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
                      {question.isBonus ? <Tag tone="good">{t.bonus}</Tag> : null}
                      {!answerable(question) ? <Tag tone="warn">{t.noAnswer}</Tag> : null}
                      {missing.length > 0 ? (
                        <Tag tone="warn">{fill(t.missingText, { n: missing.length })}</Tag>
                      ) : null}
                    </div>
                  </td>

                  <td className={cn(CELL, "text-right")}>
                    {editable ? (
                      <RowControls
                        contest={contest}
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
      </ScrollBox>

      {editable ? <AddQuestion contest={contest} dict={dict} /> : null}
    </div>
  );
}

function firstLine(question: Question): string {
  for (const text of Object.values(question.texts)) {
    const line = text.title.trim() || text.bodyMd.trim().split("\n")[0];
    if (line) return line.length > 120 ? `${line.slice(0, 120)}…` : line;
  }
  return "";
}

/** The contest as the list was rendered, whose version every control's form sends. */
type ListedAt = Pick<Contest, "id" | "version">;

/**
 * Move and delete controls. Separate forms, so the delete's confirmation,
 * pending state and refusal do not affect the moves.
 */
function RowControls({
  contest,
  questions,
  index,
  dict,
}: {
  contest: ListedAt;
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
    <div className="flex items-start justify-end gap-1">
      {index > 0 ? (
        <ReorderButton
          contest={contest}
          order={swapped(index, index - 1)}
          label={t.moveUp}
          glyph="↑"
          dict={dict}
        />
      ) : (
        <span className="inline-block size-7" />
      )}

      {index < questions.length - 1 ? (
        <ReorderButton
          contest={contest}
          order={swapped(index, index + 1)}
          label={t.moveDown}
          glyph="↓"
          dict={dict}
        />
      ) : (
        <span className="inline-block size-7" />
      )}

      <DeleteQuestion contest={contest} questionId={questions[index].id} dict={dict} />
    </div>
  );
}

function ReorderButton({
  contest,
  order,
  label,
  glyph,
  dict,
}: {
  contest: ListedAt;
  order: string[];
  label: string;
  glyph: string;
  dict: Dictionary;
}) {
  return (
    <ActionForm
      action={reorderQuestionsAction}
      {...atVersion(contest, NO_DRAFT, { order })}
      errors={dict.errors}
      className="flex flex-col items-end gap-1"
    >
      <ActionButton size="icon" variant="quiet" aria-label={label}>
        <span aria-hidden>{glyph}</span>
      </ActionButton>
      <ActionOutcome narrow />
      <ReloadWhenChanged label={dict.workspace.reload} />
    </ActionForm>
  );
}

function DeleteQuestion({
  contest,
  questionId,
  dict,
}: {
  contest: ListedAt;
  questionId: string;
  dict: Dictionary;
}) {
  const t = dict.workspace.questions;

  // Deletion is permanent, so it asks first.
  return (
    <ActionForm
      action={deleteQuestionAction}
      {...atVersion(contest, NO_DRAFT, { questionId })}
      confirm={{ title: t.confirmRemove, answer: t.remove, cancel: dict.chrome.cancel, danger: true }}
      errors={dict.errors}
      className="flex flex-col items-end gap-1"
    >
      <ActionButton size="icon" variant="quiet" aria-label={t.remove}>
        <span aria-hidden className="text-bad">
          ×
        </span>
      </ActionButton>
      <ActionOutcome narrow />
      <ReloadWhenChanged label={dict.workspace.reload} />
    </ActionForm>
  );
}

function AddQuestion({ contest, dict }: { contest: ListedAt; dict: Dictionary }) {
  const t = dict.workspace.questions;

  return (
    <ActionForm action={addQuestionAction} {...atVersion(contest, NO_DRAFT)} errors={dict.errors} className="flex flex-col gap-3">
      <ActionButton pendingLabel={t.adding} className="self-start">
        {t.add}
      </ActionButton>
      <ActionOutcome />
      <ReloadWhenChanged label={dict.workspace.reload} />
    </ActionForm>
  );
}
