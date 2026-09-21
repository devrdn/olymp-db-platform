"use client";

import { memo, useId, useState } from "react";

import type { Answers, Attempt } from "@/lib/api/query-log";
import { formatMoment, formatSeconds } from "@/lib/format/datetime";
import { cn } from "@/lib/utils";

import { QueryRow, type QueryRowLabels } from "./query-row";

/** The words the attempts are written in. */
export type AnswerAttemptsLabels = {
  empty: string;
  truncated: string;
  question: string;
  attempt: string;
  correct: string;
  wrong: string;
  points: string;
  queriesToggle: string;
  noQueries: string;
  moreQueries: string;
  window: string;
};

/**
 * Every attempt, by question in order, each opening to the queries that led
 * to it — those after the previous attempt on any question, or from the
 * start, and before this one. The API caps how many it sends per attempt and
 * counts the rest (`moreQueries`); a queries list and the CSV have them all.
 *
 * The same component on both sides of the product: a contest's staff read it
 * on the monitoring page, a participant reads their own on their report. The
 * words and the address column arrive as props.
 */
export function AnswerAttempts({
  answers,
  labels,
  queryLabels,
  statuses,
  locale,
  address = false,
}: {
  answers: Answers;
  labels: AnswerAttemptsLabels;
  /** The words of the queries under an attempt, which are a list of their own. */
  queryLabels: QueryRowLabels;
  statuses: Record<string, string>;
  locale: string;
  address?: boolean;
}) {
  const total = answers.questions.reduce((sum, question) => sum + question.attempts.length, 0);

  if (answers.questions.length === 0) return <p className="text-body text-ink-2">{labels.empty}</p>;

  return (
    <div className="flex min-w-0 flex-col gap-8">
      <div className="flex flex-col gap-1">
        <p className="text-small text-ink-3">{labels.window}</p>
        {answers.truncated ? (
          <p className="text-small text-warn">{labels.truncated.replace("{n}", String(total))}</p>
        ) : null}
      </div>
      {answers.questions.map((question) => (
        <QuestionAttempts
          key={question.questionId}
          ord={question.questionOrd}
          attempts={question.attempts}
          labels={labels}
          queryLabels={queryLabels}
          statuses={statuses}
          locale={locale}
          address={address}
        />
      ))}
    </div>
  );
}

function QuestionAttempts({
  ord,
  attempts,
  labels,
  queryLabels,
  statuses,
  locale,
  address,
}: {
  ord: number;
  attempts: Attempt[];
  labels: AnswerAttemptsLabels;
  queryLabels: QueryRowLabels;
  statuses: Record<string, string>;
  locale: string;
  address: boolean;
}) {
  const headingId = useId();
  return (
    <section aria-labelledby={headingId} className="flex min-w-0 flex-col gap-2">
      <h3 id={headingId} className="text-h3 text-ink">
        {labels.question.replace("{n}", String(ord))}
      </h3>
      <ol className="flex min-w-0 flex-col border-t border-line">
        {attempts.map((attempt) => (
          <AttemptRow
            key={attempt.id}
            attempt={attempt}
            labels={labels}
            queryLabels={queryLabels}
            statuses={statuses}
            locale={locale}
            address={address}
          />
        ))}
      </ol>
    </section>
  );
}

/** One attempt; memoised, with its expansion as its own state. */
const AttemptRow = memo(function AttemptRow({
  attempt,
  labels,
  queryLabels,
  statuses,
  locale,
  address,
}: {
  attempt: Attempt;
  labels: AnswerAttemptsLabels;
  queryLabels: QueryRowLabels;
  statuses: Record<string, string>;
  locale: string;
  address: boolean;
}) {
  const [open, setOpen] = useState(false);
  const listId = useId();
  const count = attempt.queries.length + attempt.moreQueries;

  return (
    <li className="flex min-w-0 flex-col gap-2 border-b border-line py-3">
      <div className="flex min-w-0 flex-wrap items-baseline gap-x-4 gap-y-1">
        <span className="font-mono text-label text-ink-3 uppercase">
          {labels.attempt.replace("{n}", String(attempt.attemptNo))}
        </span>
        <time
          dateTime={attempt.submittedAt}
          title={formatMoment(attempt.submittedAt, { locale })}
          className="font-mono text-label text-ink-3 tabular-nums"
        >
          {formatSeconds(attempt.submittedAt, { locale })}
        </time>
        <span className={cn("font-mono text-label uppercase", attempt.correct ? "text-good" : "text-bad")}>
          {attempt.correct ? labels.correct : labels.wrong}
        </span>
        <span className="font-mono text-label text-ink-2 tabular-nums">
          {labels.points.replace("{n}", String(attempt.points))}
        </span>
      </div>
      <p className="min-w-0 font-mono text-data break-words whitespace-pre-wrap text-ink">{attempt.value}</p>
      <button
        type="button"
        aria-expanded={open}
        aria-controls={listId}
        onClick={() => setOpen((current) => !current)}
        className="self-start text-small text-ink-2 underline-offset-4 hover:text-ink hover:underline"
      >
        {labels.queriesToggle.replace("{n}", String(count))}
      </button>
      <div id={listId} className="flex min-w-0 flex-col gap-2 border-l-2 border-line pl-3 empty:hidden max-narrow:pl-2">
        {open ? (
          attempt.queries.length === 0 && attempt.moreQueries === 0 ? (
            <p className="text-small text-ink-3">{labels.noQueries}</p>
          ) : (
            <>
              {/* Oldest first; the API keeps the first ones and counts the
                  later ones it leaves out. */}
              <ol className="flex min-w-0 flex-col">
                {attempt.queries.map((query) => (
                  <QueryRow
                    key={query.cursor}
                    query={query}
                    labels={queryLabels}
                    statuses={statuses}
                    locale={locale}
                    address={address}
                  />
                ))}
              </ol>
              {attempt.moreQueries > 0 ? (
                <p className="text-small text-ink-3">{labels.moreQueries.replace("{n}", String(attempt.moreQueries))}</p>
              ) : null}
            </>
          )
        ) : null}
      </div>
    </li>
  );
});
