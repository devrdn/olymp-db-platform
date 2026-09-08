"use client";

import { useActionState, useCallback, useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Tag } from "@/components/ui/tag";
import type { Dictionary } from "@/lib/i18n/dictionary";
import type { PlayQuestion } from "@/lib/api/play";
import { cn } from "@/lib/utils";

import { refreshQuestionsAction, submitAnswerAction, type AnswerState } from "./actions";

/**
 * One question's data, paired with its wording already rendered — see
 * QuestionsPanel's own doc for why the rendering happens before this ever
 * reaches the client.
 *
 * `index` is the question's own place in this list, 1-based — rendered as a
 * sibling of `body` rather than folded into the Markdown that produced it
 * (finding 6): `body` is the result of running the question's own wording
 * through a Markdown parser, and a question that opens with a heading, a
 * list or a fenced block has that block broken by whatever text is
 * concatenated in front of it. Keeping the number as its own element means
 * it can never collide with the structure of whatever the question's own
 * wording turns out to be.
 */
export type QuestionEntry = { question: PlayQuestion; index: number; body: ReactNode };

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
  // Whether the last re-read this panel asked for was refused (finding 6): a
  // sequential contest that just closed its open question depends on this
  // refetch to unlock the next one, and a refusal here previously vanished
  // silently — the next question stayed locked with nothing on screen saying
  // a reload would fix it.
  const [refreshFailed, setRefreshFailed] = useState(false);

  // A question closing is the one moment that can change what the rest of
  // the list looks like — a sequential contest opens the next one the
  // instant this one is done with — and the events channel has no push for
  // that, so this asks once, directly, rather than guessing which other row
  // to update. Only the mutable fields are replaced: `body` came from the
  // server once and a question's own wording never changes underneath it, so
  // there is no reason to ask for it — or to parse it — a second time.
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

  // The question the participant is working on: the first one still open to
  // them. Decided here rather than in the card, because a card cannot see the
  // others and "current" is a fact about the list.
  const currentIndex = entries.find(({ question }) => !question.closed && question.canAnswer)?.index;

  return (
    <div className="flex flex-col gap-4">
      {refreshFailed ? (
        <p role="alert" className="border border-line-2 bg-sunk p-2.5 text-small text-ink">
          {t.refreshFailed}
        </p>
      ) : null}
      {/* A ruled list, not a stack of boxes: this direction draws its
          structure from dividers rather than cards (Band's own doc), and a
          register of questions is exactly the register this system already
          keeps everything else in. */}
      <div className="flex flex-col divide-y divide-line">
        {entries.map(({ question, index, body }) => (
          <div
            key={question.id}
            // The mark the design draws as a wash, said out loud as well. A
            // row distinguished only by a background is undistinguished for
            // anybody not looking at it — the same reasoning the workspace
            // navigation already applies to its own current section.
            aria-current={index === currentIndex ? "step" : undefined}
            className={cn(
              "px-3 py-5 first:pt-3 last:pb-3",
              // The one the participant is on, marked the way the design
              // marks it: a wash, not a border. Everything on this screen is
              // already separated by rules, and a second kind of line here
              // would read as a nested box (SPEC.md §3: no nested plates).
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
              dict={dict}
              onClosed={onClosed}
            />
          </div>
        ))}
      </div>
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
  // Same fallback for the verdict itself (finding 5): a page load or a
  // reload has no submission of its own to read a verdict from, only what
  // the server's own projection already carries for a question closed on an
  // earlier visit — correct or not, and for how many points.
  const correct = state.kind === "answer" ? state.result.correct : question.correct;
  const pointsAwarded = state.kind === "answer" ? state.result.pointsAwarded : question.pointsAwarded;
  const locked = !closed && !question.canAnswer;

  const onClosedRef = useRef(onClosed);
  useEffect(() => {
    onClosedRef.current = onClosed;
  }, [onClosed]);

  // The field is controlled, rather than left to the browser (finding 3):
  // React's own form Actions reset an uncontrolled field's DOM value the
  // instant the action settles, for every outcome — a refusal included. Left
  // alone, `attempt_conflict` and `query_too_often` both say "try again"
  // while the very thing they ask the student to try again with has already
  // been deleted out from under them. Controlling it is what lets this
  // component decide, rather than the browser: cleared once an attempt is
  // actually recorded (right or wrong — a wrong guess still spent one, and
  // leaving it under the verdict reads as an answer waiting to be sent
  // rather than one already judged), left untouched on a refusal.
  //
  // Bumped during render rather than from an Effect — the pattern React's own
  // docs describe for "adjust state when something changes": comparing the
  // latest value against what was last seen, and correcting the state that
  // depends on it before this render commits, rather than committing once and
  // scheduling a second render to fix it up.
  const [value, setValue] = useState("");
  const [seenState, setSeenState] = useState(state);
  if (state !== seenState) {
    setSeenState(state);
    if (state.kind === "answer") setValue("");
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
          <span className="font-mono text-label text-ink-3 uppercase">
            {t.points.replace("{n}", String(question.points))}
          </span>
        </div>
      </div>

      {closed ? (
        <div className="flex flex-col gap-1.5">
          <p className="text-small text-ink-3">{t.closed}</p>
          {/* The verdict from whatever closed it — the live submission if
              this render followed one, or the server's own record of an
              earlier one otherwise (finding 5): losing this the instant a
              question closes, or the instant the page reloads, would hide
              the one feedback a correct answer exists to give. */}
          <Verdict correct={correct} points={pointsAwarded} dict={dict} />
        </div>
      ) : (
        <form action={formAction} className="flex flex-col gap-2.5">
          <input type="hidden" name="contestId" value={contestId} />
          <input type="hidden" name="questionId" value={question.id} />

          {question.kind === "choice" ? (
            <fieldset className="flex flex-col gap-1.5" disabled={pending || locked}>
              <legend className="sr-only">{t.answerLabel}</legend>
              {question.choiceIds.map((choiceId) => (
                // `min-h-6`: the label *is* the target — clicking anywhere on
                // it selects the choice — and the row measured 22px tall, so
                // the whole answer to a multiple-choice question was a
                // sub-24px strip on every screen size. The radio itself stays
                // 16px because that is what the design draws; what has to be
                // hittable is this box around it.
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

/**
 * Which question has to close before `index` opens.
 *
 * The one immediately before it that is still open — sequential progression
 * is what makes a question unanswerable, and §6.1.1 puts the rule on the
 * server; this only names the question the server is waiting on, so the
 * participant is told "after 4" instead of "not yet".
 */
function blockedBy(entries: QuestionEntry[], index: number): number | undefined {
  for (let i = index - 2; i >= 0; i--) {
    if (!entries[i].question.closed) return entries[i].index;
  }
  return undefined;
}

/**
 * A question's state, in the one word the design's own card carries.
 *
 * Four states and not five: a question closed without a correct answer is
 * "attempts spent", which is a different sentence from "accepted" and a
 * different one again from "not answered yet" — collapsing the first two
 * would tell a participant they had solved something they had not.
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
  t: Dictionary["participant"]["play"]["questions"];
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
