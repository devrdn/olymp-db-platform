"use client";

import { useActionState, useCallback, useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import type { Dictionary } from "@/lib/i18n/dictionary";
import type { PlayQuestion } from "@/lib/api/play";
import { cn } from "@/lib/utils";

import { refreshQuestionsAction, submitAnswerAction, type AnswerState } from "./actions";

/** One question's data, paired with its wording already rendered — see QuestionsPanel's own doc for why the rendering happens before this ever reaches the client. */
export type QuestionEntry = { question: PlayQuestion; body: ReactNode };

/**
 * The questions, and the one field each has for answering.
 *
 * `body` arrives already rendered, from a Server Component (page.tsx), rather
 * than as `bodyMd` for this file to run through `StoryText` itself. `StoryText`
 * renders Markdown through `react-markdown` — a real parser, tens of
 * kilobytes gzipped — and every byte of it is free on the server and never
 * free in a client bundle. A question's wording is fixed the moment the page
 * loads, so there is nothing to gain and a whole parser to lose by asking the
 * browser to do this again.
 *
 * The state here is its own, seeded from what the page loaded and never
 * lifted higher: a submission touches this list and nothing else on the
 * screen, which is what keeps answering a question from re-rendering the
 * story or the console beside it — the smoothness the plan asks for is a
 * property of where the state lives, not something added on top.
 */
export function QuestionsPanel({
  contestId,
  items,
  dict,
}: {
  contestId: string;
  items: QuestionEntry[];
  dict: Dictionary;
}) {
  const t = dict.participant.play.questions;
  const [entries, setEntries] = useState(items);

  // A question closing is the one moment that can change what the rest of
  // the list looks like — a sequential contest opens the next one the
  // instant this one is done with — and the events channel has no push for
  // that, so this asks once, directly, rather than guessing which other row
  // to update. Only the mutable fields are replaced: `body` came from the
  // server once and a question's own wording never changes underneath it, so
  // there is no reason to ask for it — or to parse it — a second time.
  const onClosed = useCallback(async () => {
    const result = await refreshQuestionsAction(contestId);
    if (result.kind !== "ok") return;
    const byId = new Map(result.items.map((q) => [q.id, q]));
    setEntries((prev) => prev.map((entry) => {
      const fresh = byId.get(entry.question.id);
      return fresh ? { ...entry, question: fresh } : entry;
    }));
  }, [contestId]);

  if (entries.length === 0) {
    return <p className="text-body text-ink-2">{t.empty}</p>;
  }

  return (
    // A ruled list, not a stack of boxes: this direction draws its structure
    // from dividers rather than cards (Band's own doc), and a register of
    // questions is exactly the register this system already keeps everything
    // else in.
    <div className="flex flex-col divide-y divide-line">
      {entries.map(({ question, body }) => (
        <div key={question.id} className="py-5 first:pt-0 last:pb-0">
          <QuestionCard
            contestId={contestId}
            question={question}
            body={body}
            dict={dict}
            onClosed={onClosed}
          />
        </div>
      ))}
    </div>
  );
}

function QuestionCard({
  contestId,
  question,
  body,
  dict,
  onClosed,
}: {
  contestId: string;
  question: PlayQuestion;
  body: ReactNode;
  dict: Dictionary;
  onClosed: () => void;
}) {
  const t = dict.participant.play.questions;
  const [state, formAction, pending] = useActionState<AnswerState, FormData>(submitAnswerAction, {
    kind: "idle",
  });

  // The most recent submission's own word on attempts and closedness beats
  // what the page loaded with — it is strictly newer — and falls back to the
  // list's own reading until there has been one.
  const attemptsRemaining = state.kind === "answer" ? state.result.attemptsRemaining : question.attemptsRemaining;
  const closed = state.kind === "answer" ? state.result.closed : question.closed;
  const locked = !closed && !question.canAnswer;

  const onClosedRef = useRef(onClosed);
  useEffect(() => {
    onClosedRef.current = onClosed;
  }, [onClosed]);

  // The field clears after every attempt, right or wrong: a wrong guess still
  // spent one, and leaving it sitting in the box under the verdict reads as
  // an answer waiting to be sent rather than one already judged.
  //
  // Bumped during render rather than from an Effect — the pattern React's own
  // docs describe for "adjust state when something changes": comparing the
  // latest value against what was last seen, and correcting the state that
  // depends on it before this render commits, rather than committing once and
  // scheduling a second render to fix it up.
  const [seenState, setSeenState] = useState(state);
  const [formKey, setFormKey] = useState(0);
  if (state !== seenState) {
    setSeenState(state);
    if (state.kind !== "idle") setFormKey((key) => key + 1);
  }

  // Notifying the panel that this question closed *is* a side effect — a
  // network call — so unlike the field-clearing above, it belongs in an
  // Effect rather than in the render body.
  const notified = useRef(false);
  useEffect(() => {
    if (state.kind === "answer" && state.result.closed && !notified.current) {
      notified.current = true;
      onClosedRef.current();
    }
  }, [state]);

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        {body}
        <span className="shrink-0 font-mono text-label text-ink-3 uppercase">
          {t.points.replace("{n}", String(question.points))}
        </span>
      </div>

      {closed ? (
        <div className="flex flex-col gap-1.5">
          <p className="text-small text-ink-3">{t.closed}</p>
          {/* The verdict from the very submission that closed it — losing
              this the instant the question closes would hide the one
              feedback a correct final attempt exists to give. */}
          {state.kind === "answer" ? (
            <Verdict correct={state.result.correct} points={state.result.pointsAwarded} dict={dict} />
          ) : null}
        </div>
      ) : (
        <form
          action={formAction}
          className="flex flex-col gap-2.5"
          key={formKey}
        >
          <input type="hidden" name="contestId" value={contestId} />
          <input type="hidden" name="questionId" value={question.id} />

          {question.kind === "choice" ? (
            <fieldset className="flex flex-col gap-1.5" disabled={pending || locked}>
              <legend className="sr-only">{t.answerLabel}</legend>
              {question.choiceIds.map((choiceId) => (
                <label key={choiceId} className="flex cursor-pointer items-baseline gap-2.5 text-control text-ink">
                  <input
                    type="radio"
                    name="value"
                    value={choiceId}
                    disabled={pending || locked}
                    className="size-4 cursor-pointer accent-cta disabled:cursor-not-allowed"
                  />
                  {question.choices[choiceId] ?? choiceId}
                </label>
              ))}
            </fieldset>
          ) : (
            <Input
              name="value"
              placeholder={t.placeholder}
              disabled={pending || locked}
              aria-label={t.answerLabel}
              autoComplete="off"
            />
          )}

          <div className="flex flex-wrap items-center gap-3">
            <Button type="submit" size="sm" disabled={pending || locked}>
              {pending ? t.submitting : t.submit}
            </Button>

            {locked ? (
              <span className="text-small text-ink-3">{t.locked}</span>
            ) : attemptsRemaining !== undefined ? (
              <span className="text-small text-ink-2">
                {attemptsRemaining > 0
                  ? t.attemptsLeft.replace("{n}", String(attemptsRemaining))
                  : t.noAttempts}
              </span>
            ) : null}
          </div>

          {state.kind === "answer" ? <Verdict correct={state.result.correct} points={state.result.pointsAwarded} dict={dict} /> : null}
          {state.kind === "refused" ? <Refusal state={state} dict={dict} /> : null}
        </form>
      )}
    </div>
  );
}

function Verdict({ correct, points, dict }: { correct: boolean; points: number; dict: Dictionary }) {
  const t = dict.participant.play.questions;
  return (
    <p role="status" className={cn("text-small", correct ? "text-good" : "text-ink-2")}>
      {correct ? t.correct.replace("{n}", String(points)) : t.incorrect}
    </p>
  );
}

/** Why an answer did not go through — the same shape and the same reasoning as the console's own refusal. */
function Refusal({ state, dict }: { state: Extract<AnswerState, { kind: "refused" }>; dict: Dictionary }) {
  const t = dict.participant.play.questions;
  const errors = dict.errors as Record<string, string>;
  const message = errors[state.code] ?? errors.fallback;

  const passing = ["query_too_often", "attempt_conflict"].includes(state.code);

  return (
    <div
      role="status"
      className={cn("border p-2.5 text-small", passing ? "border-edge bg-sunk text-ink" : "border-bad/40 bg-bad-wash text-ink")}
    >
      {message}
      {state.subject ? <span className="ml-1 font-mono text-ink-2">{state.subject}</span> : null}
      {state.requestId && !passing ? (
        <p className="mt-1.5 font-mono text-ink-3">{t.reference.replace("{id}", state.requestId)}</p>
      ) : null}
    </div>
  );
}
