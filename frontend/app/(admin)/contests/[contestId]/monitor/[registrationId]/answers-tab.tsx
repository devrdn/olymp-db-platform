"use client";

import { memo, useId, useState } from "react";

import type { Answers, Attempt } from "@/lib/api/monitor";
import { formatMoment } from "@/lib/format/datetime";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { clock } from "../live-feed";
import { QueryRow } from "./query-row";

/**
 * The answers tab (design §3, §6): every attempt, by question in order, each
 * opening to the queries that led to it — those after the participant's
 * previous attempt on any question, or from the start, and before this one.
 * The API caps how many it sends per attempt and counts the rest
 * (`moreQueries`); the queries tab and the CSV have them all.
 */
export function AnswersTab({ answers, dict, locale }: { answers: Answers; dict: Dictionary; locale: string }) {
  const t = dict.workspace.monitor.participant.answers;
  const total = answers.questions.reduce((sum, question) => sum + question.attempts.length, 0);

  if (answers.questions.length === 0) return <p className="text-body text-ink-2">{t.empty}</p>;

  return (
    <div className="flex min-w-0 flex-col gap-8">
      <div className="flex flex-col gap-1">
        <p className="text-small text-ink-3">{t.window}</p>
        {answers.truncated ? (
          <p className="text-small text-warn">{t.truncated.replace("{n}", String(total))}</p>
        ) : null}
      </div>
      {answers.questions.map((question) => (
        <QuestionAttempts
          key={question.questionId}
          ord={question.questionOrd}
          attempts={question.attempts}
          dict={dict}
          locale={locale}
        />
      ))}
    </div>
  );
}

function QuestionAttempts({
  ord,
  attempts,
  dict,
  locale,
}: {
  ord: number;
  attempts: Attempt[];
  dict: Dictionary;
  locale: string;
}) {
  const t = dict.workspace.monitor.participant.answers;
  const headingId = useId();
  return (
    <section aria-labelledby={headingId} className="flex min-w-0 flex-col gap-2">
      <h3 id={headingId} className="text-h3 text-ink">
        {t.question.replace("{n}", String(ord))}
      </h3>
      <ol className="flex min-w-0 flex-col border-t border-line">
        {attempts.map((attempt) => (
          <AttemptRow key={attempt.id} attempt={attempt} dict={dict} locale={locale} />
        ))}
      </ol>
    </section>
  );
}

/** One attempt; memoised, with its expansion as its own state. */
const AttemptRow = memo(function AttemptRow({
  attempt,
  dict,
  locale,
}: {
  attempt: Attempt;
  dict: Dictionary;
  locale: string;
}) {
  const t = dict.workspace.monitor.participant.answers;
  const [open, setOpen] = useState(false);
  const listId = useId();
  const count = attempt.queries.length + attempt.moreQueries;

  return (
    <li className="flex min-w-0 flex-col gap-2 border-b border-line py-3">
      <div className="flex min-w-0 flex-wrap items-baseline gap-x-4 gap-y-1">
        <span className="font-mono text-label text-ink-3 uppercase">
          {t.attempt.replace("{n}", String(attempt.attemptNo))}
        </span>
        <time
          dateTime={attempt.submittedAt}
          title={formatMoment(attempt.submittedAt, { locale })}
          className="font-mono text-label text-ink-3 tabular-nums"
        >
          {clock(attempt.submittedAt, locale)}
        </time>
        <span className={cn("font-mono text-label uppercase", attempt.correct ? "text-good" : "text-bad")}>
          {attempt.correct ? t.correct : t.wrong}
        </span>
        <span className="font-mono text-label text-ink-2 tabular-nums">
          {t.points.replace("{n}", String(attempt.points))}
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
        {t.queriesToggle.replace("{n}", String(count))}
      </button>
      <div id={listId} className="flex min-w-0 flex-col gap-2 border-l-2 border-line pl-3 empty:hidden max-narrow:pl-2">
        {open ? (
          attempt.queries.length === 0 && attempt.moreQueries === 0 ? (
            <p className="text-small text-ink-3">{t.noQueries}</p>
          ) : (
            <>
              {/* Oldest first; the API keeps the first ones and counts the
                  later ones it leaves out. */}
              <ol className="flex min-w-0 flex-col">
                {attempt.queries.map((query) => (
                  <QueryRow key={query.cursor} query={query} dict={dict} locale={locale} />
                ))}
              </ol>
              {attempt.moreQueries > 0 ? (
                <p className="text-small text-ink-3">{t.moreQueries.replace("{n}", String(attempt.moreQueries))}</p>
              ) : null}
            </>
          )
        ) : null}
      </div>
    </li>
  );
});
