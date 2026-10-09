"use client";

import { useActionState, useCallback, useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Tag } from "@/components/ui/tag";
import type { PlayDictionary } from "./dictionary";
import type { PlayQuestion } from "@/lib/api/play";
import type { Scoring } from "@/lib/api/contests";
import { cn } from "@/lib/utils";

import { refreshQuestionsAction, submitAnswerAction, type AnswerState } from "./actions";
import { messageForCode } from "@/lib/i18n/errors";
import { refusalKind, showsReference } from "./refusals";

/**
 * One question's data with its wording already rendered on the server.
 * `index` is its 1-based place in the list, rendered beside `body` rather
 * than prepended to the Markdown, which would break a question opening with
 * a heading, a list or a fenced block.
 */
export type QuestionEntry = { question: PlayQuestion; index: number; body: ReactNode };

/**
 * The questions, and the one field each has for answering. `body` arrives
 * rendered by `page.tsx`. The list's state lives here and nowhere higher, so
 * answering a question re-renders neither the story nor the console.
 */
export function QuestionsPanel({
  contestId,
  items,
  // Defaulted for tests; `play/page.tsx` always passes it.
  scoring = "points",
  icpcPenaltyMin = 20,
  dict,
}: {
  contestId: string;
  items: QuestionEntry[];
  scoring?: Scoring;
  /** Minutes added to the penalty time per wrong attempt on a question later solved. Read only under `icpc`. */
  icpcPenaltyMin?: number;
  dict: PlayDictionary;
}) {
  const t = dict.participant.play.questions;
  const [entries, setEntries] = useState(items);
  // Whether the last re-read was refused. A sequential contest needs it to
  // unlock the next question, so a failure is shown with a hint to reload.
  const [refreshFailed, setRefreshFailed] = useState(false);

  // A closed question can open the next one in a sequential contest, and the
  // events channel has no push for that, so the list is re-read once. Only
  // the mutable fields are replaced; the wording never changes.
  const onClosed = useCallback(async () => {
    const result = await refreshQuestionsAction(contestId);
    if (result.kind !== "ok") {
      setRefreshFailed(true);
      return;
    }
    setRefreshFailed(false);
    const byId = new Map(result.items.map((q) => [q.id, q]));
    setEntries((prev) => prev.map((entry) => {
      const fresh = byId.get(entry.question.id);
      return fresh ? { ...entry, question: fresh } : entry;
    }));
  }, [contestId]);

  if (entries.length === 0) {
    return <p className="text-body text-ink-2">{t.empty}</p>;
  }

  // The first question still open to the participant; decided here because
  // "current" is a fact about the list.
  const currentIndex = entries.find(({ question }) => !question.closed && question.canAnswer)?.index;

  return (
    <div className="flex flex-col gap-4">
      {refreshFailed ? (
        <p role="alert" className="border border-line-2 bg-sunk p-2.5 text-small text-ink">
          {t.refreshFailed}
        </p>
      ) : null}
      {/* A ruled list rather than boxes: this design draws structure with
          dividers (Band's doc). */}
      <div className="flex flex-col divide-y divide-line">
        {entries.map(({ question, index, body }) => (
          <div
            key={question.id}
            // The design's wash, also said to assistive technology.
            aria-current={index === currentIndex ? "step" : undefined}
            className={cn(
              "px-3 py-5 first:pt-3 last:pb-3",
              // A wash, not a border: a second kind of line would read as a nested
              // box (SPEC.md §5).
              index === currentIndex && "bg-accent-wash",
            )}
          >
            <QuestionCard
              contestId={contestId}
              question={question}
              index={index}
              body={body}
              current={index === currentIndex}
              blockedBy={index === currentIndex ? undefined : blockedBy(entries, index)}
              scoring={scoring}
              dict={dict}
              onClosed={onClosed}
            />
          </div>
        ))}
      </div>
      {/* Once for the list: the penalty belongs to the contest
          (docs/ARCHITECTURE.md §6.1.1). */}
      {scoring === "icpc" ? (
        <p className="text-small text-ink-3">{t.icpcPenalty.replace("{n}", String(icpcPenaltyMin))}</p>
      ) : null}
    </div>
  );
}

function QuestionCard({
  contestId,
  question,
  index,
  body,
  current,
  blockedBy,
  scoring,
  dict,
  onClosed,
}: {
  contestId: string;
  question: PlayQuestion;
  index: number;
  body: ReactNode;
  /** Whether this is the question the participant is working on. */
  current: boolean;
  /** The question that has to close before this one opens, if any. */
  blockedBy?: number;
  scoring: Scoring;
  dict: PlayDictionary;
  onClosed: () => void;
}) {
  const t = dict.participant.play.questions;
  const [state, formAction, pending] = useActionState<AnswerState, FormData>(submitAnswerAction, {
    kind: "idle",
  });

  // The latest submission is newer than what the page loaded, so it wins.
  const attemptsRemaining = state.kind === "answer" ? state.result.attemptsRemaining : question.attemptsRemaining;
  const closed = state.kind === "answer" ? state.result.closed : question.closed;
  // Same fallback for the verdict: after a reload only the server's record
  // of an earlier close can supply it.
  const correct = state.kind === "answer" ? state.result.correct : question.correct;
  const pointsAwarded = state.kind === "answer" ? state.result.pointsAwarded : question.pointsAwarded;
  const locked = !closed && !question.canAnswer;

  const onClosedRef = useRef(onClosed);
  useEffect(() => {
    onClosedRef.current = onClosed;
  }, [onClosed]);

  // Controlled because React's form Actions reset an uncontrolled field when
  // the action settles, refusals included, and `attempt_conflict` or
  // `query_too_often` would then ask for a retry of text already erased.
  // Cleared once an attempt is recorded, right or wrong; kept on a refusal.
  // Adjusted during render, React's pattern for resetting state on a change.
  const [value, setValue] = useState("");
  const [seenState, setSeenState] = useState(state);
  if (state !== seenState) {
    setSeenState(state);
    if (state.kind === "answer") setValue("");
  }

  // Notifying the panel is a network call, so it belongs in an Effect.
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
        <div className="flex min-w-0 items-baseline gap-2">
          <span aria-hidden="true" className="shrink-0 font-mono text-label text-ink-3">
            {index}.
          </span>
          <span className="sr-only">{t.numberLabel.replace("{n}", String(index))}</span>
          <div className="min-w-0">{body}</div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <QuestionStatus
            closed={closed}
            correct={correct}
            current={current}
            locked={locked}
            blockedBy={blockedBy}
            t={t}
          />
          {/* ICPC never awards points (docs/ARCHITECTURE.md §6.1.1), so none
              are shown rather than a meaningless zero. */}
          {scoring !== "icpc" ? (
            <span className="font-mono text-label text-ink-3 uppercase">
              {t.points.replace("{n}", String(question.points))}
            </span>
          ) : null}
        </div>
      </div>

      {closed ? (
        <div className="flex flex-col gap-1.5">
          <p className="text-small text-ink-3">{t.closed}</p>
          {/* The verdict from the live submission or, after a reload, the
              server's record of it. */}
          <Verdict correct={correct} points={pointsAwarded} scoring={scoring} dict={dict} />
        </div>
      ) : (
        <form action={formAction} className="flex flex-col gap-2.5">
          <input type="hidden" name="contestId" value={contestId} />
          <input type="hidden" name="questionId" value={question.id} />

          {question.kind === "choice" ? (
            <fieldset className="flex flex-col gap-1.5" disabled={pending || locked}>
              <legend className="sr-only">{t.answerLabel}</legend>
              {question.choiceIds.map((choiceId) => (
                // `min-h-6`: the label is the click target and must reach 24px; the
                // radio itself stays the design's 16px.
                <label
                  key={choiceId}
                  className="flex min-h-6 cursor-pointer items-baseline gap-2.5 text-control text-ink"
                >
                  <input
                    type="radio"
                    name="value"
                    value={choiceId}
                    checked={value === choiceId}
                    onChange={() => setValue(choiceId)}
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
              value={value}
              onChange={(event) => setValue(event.target.value)}
              placeholder={t.placeholder}
              disabled={pending || locked}
              aria-label={t.answerLabel}
              autoComplete="off"
              // Pastes here are reported to the organiser (use-signals.ts).
              data-paste-target="answer"
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

          {state.kind === "answer" ? (
            <Verdict correct={state.result.correct} points={state.result.pointsAwarded} scoring={scoring} dict={dict} />
          ) : null}
          {state.kind === "refused" ? <Refusal state={state} dict={dict} /> : null}
        </form>
      )}
    </div>
  );
}

function Verdict({
  correct,
  points,
  scoring,
  dict,
}: {
  correct: boolean;
  points: number;
  scoring: Scoring;
  dict: PlayDictionary;
}) {
  const t = dict.participant.play.questions;
  // ICPC awards no points (`points_awarded` is always 0), so none are
  // announced.
  const correctText = scoring === "icpc" ? t.correctIcpc : t.correct.replace("{n}", String(points));
  return (
    <p role="status" className={cn("text-small", correct ? "text-good" : "text-ink-2")}>
      {correct ? correctText : t.incorrect}
    </p>
  );
}

/** Why an answer did not go through; the same shape as the console's refusal. */
function Refusal({ state, dict }: { state: Extract<AnswerState, { kind: "refused" }>; dict: PlayDictionary }) {
  const t = dict.participant.play.questions;
  const message = messageForCode(state.code, dict.errors);

  // A wait rather than a fault: asking again later is the remedy, so these
  // read quietly.
  const passing = refusalKind(state.code) === "passing";

  return (
    <div
      role="status"
      className={cn("border p-2.5 text-small", passing ? "border-edge bg-sunk text-ink" : "border-bad/40 bg-bad-wash text-ink")}
    >
      {message}
      {state.subject ? <span className="ml-1 font-mono text-ink-2">{state.subject}</span> : null}
      {state.requestId && showsReference(state.code, dict.errors) ? (
        <p className="mt-1.5 font-mono text-ink-3">{t.reference.replace("{id}", state.requestId)}</p>
      ) : null}
    </div>
  );
}

/**
 * The nearest earlier question still open, which must close before `index`
 * opens. The server enforces sequential order (docs/ARCHITECTURE.md
 * §6.1.1); this only names the question it waits on, so the participant
 * reads "after 4", not "not yet".
 */
function blockedBy(entries: QuestionEntry[], index: number): number | undefined {
  for (let i = index - 2; i >= 0; i--) {
    if (!entries[i].question.closed) return entries[i].index;
  }
  return undefined;
}

/**
 * A question's state in one word. A question closed without a correct answer
 * is "attempts spent", never "accepted".
 */
function QuestionStatus({
  closed,
  correct,
  current,
  locked,
  blockedBy,
  t,
}: {
  closed: boolean;
  correct?: boolean;
  current: boolean;
  locked: boolean;
  blockedBy?: number;
  t: PlayDictionary["participant"]["play"]["questions"];
}) {
  if (closed) {
    return correct ? <Tag tone="good">{t.status.accepted}</Tag> : <Tag tone="mute">{t.status.spent}</Tag>;
  }
  if (locked) {
    return (
      <Tag tone="mute">
        {blockedBy !== undefined ? t.status.after.replace("{n}", String(blockedBy)) : t.status.open}
      </Tag>
    );
  }
  return current ? <Tag tone="ink">{t.status.current}</Tag> : <Tag tone="mute">{t.status.open}</Tag>;
}
