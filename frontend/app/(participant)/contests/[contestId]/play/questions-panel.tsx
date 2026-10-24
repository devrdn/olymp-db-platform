"use client";

import { Suspense, lazy, useActionState, useCallback, useEffect, useId, useRef, useState } from "react";
import type { ReactNode } from "react";
import { ChevronRight, Lock, Maximize2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Tag } from "@/components/ui/tag";
import type { PlayDictionary } from "./dictionary";
import type { LockedPlayQuestion, OpenPlayQuestion, PlayQuestion } from "@/lib/api/play";
import type { Scoring } from "@/lib/api/contests";
import type { Locale } from "@/lib/i18n/config";
import { cn } from "@/lib/utils";

import { refreshQuestions, submitAnswer, type AnswerState } from "./requests";
import { messageForCode } from "@/lib/i18n/errors";
import { refusalKind, showsReference } from "./refusals";

/**
 * One question's data with its wording already rendered on the server.
 * `index` is its 1-based place in the list, rendered beside `body` rather
 * than prepended to the Markdown, which would break a question opening with
 * a heading, a list or a fenced block. A locked question has no wording to
 * render.
 */
export type QuestionEntry =
  | { question: OpenPlayQuestion; index: number; body: ReactNode }
  | { question: LockedPlayQuestion; index: number; body: null };

type OpenEntry = Extract<QuestionEntry, { question: OpenPlayQuestion }>;

// Fetched only once a question opens after the page loaded; every body the
// page loads with was rendered on the server.
const LazyQuestionBody = lazy(() => import("./question-body").then((m) => ({ default: m.QuestionBody })));

/**
 * The entries for a re-read list. A body already on screen is kept, so the
 * Markdown is not parsed again; a question that has just opened gets one
 * rendered here.
 */
function entriesFrom(items: PlayQuestion[], previous: QuestionEntry[]): QuestionEntry[] {
  const bodies = new Map<string, ReactNode>();
  for (const entry of previous) {
    if (!entry.question.locked) bodies.set(entry.question.id, entry.body);
  }
  return items.map((question, i): QuestionEntry => {
    if (question.locked) return { question, index: question.position, body: null };
    const body = bodies.has(question.id) ? bodies.get(question.id) : (
      <Suspense fallback={<div aria-hidden="true" className="h-5 w-2/3 animate-pulse bg-sunk" />}>
        <LazyQuestionBody markdown={question.bodyMd} />
      </Suspense>
    );
    return { question, index: i + 1, body };
  });
}

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
  locale,
}: {
  contestId: string;
  items: QuestionEntry[];
  scoring?: Scoring;
  /** Minutes added to the penalty time per wrong attempt on a question later solved. Read only under `icpc`. */
  icpcPenaltyMin?: number;
  dict: PlayDictionary;
  /** The language the questions were rendered in; a re-read asks for the same. */
  locale: Locale;
}) {
  const t = dict.participant.play.questions;
  const [entries, setEntries] = useState(items);
  // Whether the last re-read was refused. A sequential contest needs it to
  // unlock the next question, so a failure is shown with a hint to reload.
  const [refreshFailed, setRefreshFailed] = useState(false);

  // A closed question can open the next one in a sequential contest, and the
  // events channel has no push for that, so the list is re-read once.
  const onClosed = useCallback(async () => {
    const result = await refreshQuestions(contestId, locale);
    if (result.kind !== "ok") {
      setRefreshFailed(true);
      return;
    }
    setRefreshFailed(false);
    setEntries((prev) => entriesFrom(result.items, prev));
  }, [contestId, locale]);

  if (entries.length === 0) {
    return <p className="text-body text-ink-2">{t.empty}</p>;
  }

  const open = entries.filter((entry): entry is OpenEntry => !entry.question.locked);
  const locked = entries.filter((entry) => entry.question.locked);
  // The first question still open to the participant; decided here because
  // "current" is a fact about the list.
  const currentIndex = open.find(({ question }) => !question.closed && question.canAnswer)?.index;

  return (
    <div className="flex flex-col gap-4">
      {refreshFailed ? (
        <p role="alert" className="border border-line-2 bg-sunk p-2.5 text-small text-ink">
          {t.refreshFailed}
        </p>
      ) : null}
      {/* A ruled list rather than boxes: this design draws structure with
          dividers (Band's doc). */}
      {open.length > 0 ? (
        <div className="flex flex-col divide-y divide-line">
          {open.map(({ question, index, body }) => (
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
                scoring={scoring}
                dict={dict}
                onClosed={onClosed}
              />
            </div>
          ))}
        </div>
      ) : null}
      {/* Numbers only: the server sent nothing else. Plain text, never a
          control, so the keyboard passes them by while a screen reader reads
          them; `role` is restated because Safari drops a list's role once its
          bullets are styled away. The list follows every open question, even
          a closed one numbered past it (an attempt cap raised mid-contest can
          reopen an earlier question), so what is not open yet stays one
          region. */}
      {locked.length > 0 ? (
        <ul role="list" aria-label={t.lockedList} className="flex flex-col gap-1.5 px-3">
          {locked.map(({ index }) => (
            <li key={index} className="flex items-baseline gap-2 text-small text-ink-3">
              <Lock aria-hidden="true" className="size-3.5 shrink-0 self-center" />
              {index > 1
                ? t.lockedItem.replace("{n}", String(index)).replace("{m}", String(index - 1))
                : t.lockedNotYet.replace("{n}", String(index))}
            </li>
          ))}
        </ul>
      ) : null}
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
  scoring,
  dict,
  onClosed,
}: {
  contestId: string;
  question: OpenPlayQuestion;
  index: number;
  body: ReactNode;
  /** Whether this is the question the participant is working on. */
  current: boolean;
  scoring: Scoring;
  dict: PlayDictionary;
  onClosed: () => void;
}) {
  const t = dict.participant.play.questions;
  const [state, formAction, pending] = useActionState<AnswerState, FormData>(submitAnswer, {
    kind: "idle",
  });

  // The latest submission is newer than what the page loaded, so it wins.
  const attemptsRemaining = state.kind === "answer" ? state.result.attemptsRemaining : question.attemptsRemaining;
  const closed = state.kind === "answer" ? state.result.closed : question.closed;
  // Same fallback for the verdict: after a reload only the server's record
  // of an earlier close can supply it.
  const correct = state.kind === "answer" ? state.result.correct : question.correct;
  const pointsAwarded = state.kind === "answer" ? state.result.pointsAwarded : question.pointsAwarded;
  // Not expected from the server, which locks a question by withholding it,
  // but a refusal it states is still shown rather than answered into.
  const unanswerable = !closed && !question.canAnswer;
  // A title of spaces is no title: the wording then shows unfolded.
  const title = question.title.trim();
  const titleId = useId();

  const onClosedRef = useRef(onClosed);
  useEffect(() => {
    onClosedRef.current = onClosed;
  }, [onClosed]);

  // Controlled because React's form Actions reset an uncontrolled field when
  // the action settles, refusals included, and `attempt_conflict` or
  // `play_read_too_often` would then ask for a retry of text already erased.
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
          {title ? (
            <h2 id={titleId} className="min-w-0 text-row text-ink [overflow-wrap:anywhere]">
              {title}
            </h2>
          ) : (
            <div className="min-w-0">{body}</div>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <QuestionStatus
            closed={closed}
            correct={correct}
            current={current}
            t={t}
          />
          {question.isBonus ? <Tag tone="good">{t.bonus}</Tag> : null}
          {/* ICPC never awards points (docs/ARCHITECTURE.md §6.1.1), so none
              are shown rather than a meaningless zero. */}
          {scoring !== "icpc" ? (
            <span className="font-mono text-label text-ink-3 uppercase">
              {(question.isBonus ? t.bonusPoints : t.points).replace("{n}", String(question.points))}
            </span>
          ) : null}
        </div>
      </div>

      {question.isBonus && !closed ? (
        <p className="text-small text-ink-2 [overflow-wrap:anywhere]">
          {t.bonusHint.replace("{n}", String(question.points))}
        </p>
      ) : null}

      {title ? <FoldedBody title={title} titleId={titleId} body={body} dict={dict} /> : null}

      {closed ? (
        <div className="flex flex-col gap-1.5">
          <p className="text-small text-ink-3">{t.closed}</p>
          {/* The verdict from the live submission or, after a reload, the
              server's record of it. */}
          <Verdict correct={correct} points={pointsAwarded} bonus={bonusOf(question)} scoring={scoring} dict={dict} />
        </div>
      ) : (
        <form action={formAction} className="flex flex-col gap-2.5">
          <input type="hidden" name="contestId" value={contestId} />
          <input type="hidden" name="questionId" value={question.id} />

          {question.kind === "choice" ? (
            <fieldset className="flex flex-col gap-1.5" disabled={pending || unanswerable}>
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
                    disabled={pending || unanswerable}
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
              disabled={pending || unanswerable}
              aria-label={t.answerLabel}
              autoComplete="off"
              // Pastes here are reported to the organiser (use-signals.ts).
              data-paste-target="answer"
            />
          )}

          <div className="flex flex-wrap items-center gap-3">
            <Button type="submit" size="sm" disabled={pending || unanswerable}>
              {pending ? t.submitting : t.submit}
            </Button>

            {unanswerable ? (
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
            <Verdict
              correct={state.result.correct}
              points={state.result.pointsAwarded}
              bonus={bonusOf(question)}
              scoring={scoring}
              dict={dict}
            />
          ) : null}
          {state.kind === "refused" ? <Refusal state={state} dict={dict} /> : null}
        </form>
      )}
    </div>
  );
}

/**
 * A titled question's wording, folded under the title: opened in place by a
 * disclosure button, or read larger in a dialog. Both controls are described
 * by the title, so a screen reader moving between questions hears which one
 * each opens. The answer form sits outside, never folded away.
 */
function FoldedBody({
  title,
  titleId,
  body,
  dict,
}: {
  title: string;
  titleId: string;
  body: ReactNode;
  dict: PlayDictionary;
}) {
  const t = dict.participant.play.questions;
  const bodyId = useId();
  const [expanded, setExpanded] = useState(false);
  const [enlarged, setEnlarged] = useState(false);

  return (
    <div className="flex flex-col gap-2">
      <div className="-ml-3 flex flex-wrap items-center gap-1">
        <Button
          type="button"
          variant="quiet"
          size="sm"
          aria-expanded={expanded}
          aria-controls={bodyId}
          aria-describedby={titleId}
          onClick={() => setExpanded((open) => !open)}
        >
          <ChevronRight
            aria-hidden="true"
            className={cn("transition-transform duration-(--t-input) ease-standard", expanded && "rotate-90")}
          />
          {t.details}
        </Button>
        <Button
          type="button"
          variant="quiet"
          size="sm"
          aria-haspopup="dialog"
          aria-describedby={titleId}
          onClick={() => setEnlarged(true)}
        >
          <Maximize2 aria-hidden="true" />
          {t.openLarger}
        </Button>
      </div>

      <div id={bodyId} hidden={!expanded} className="min-w-0">
        {body}
      </div>

      <Dialog open={enlarged} onOpenChange={setEnlarged}>
        <DialogContent
          closeLabel={t.closeLarger}
          className="max-h-[calc(100dvh-2rem)] w-[calc(100%-2rem)] max-w-3xl overflow-y-auto"
        >
          <DialogHeader>
            <DialogTitle className="[overflow-wrap:anywhere]">{title}</DialogTitle>
          </DialogHeader>
          <div className="min-w-0">{body}</div>
        </DialogContent>
      </Dialog>
    </div>
  );
}

/**
 * The most a correct bonus answer gives back, or undefined for a regular
 * question. What it actually gives back depends on penalties the table
 * counts, so the verdict can only say "up to".
 */
function bonusOf(question: OpenPlayQuestion): number | undefined {
  return question.isBonus ? question.points : undefined;
}

function Verdict({
  correct,
  points,
  bonus,
  scoring,
  dict,
}: {
  correct: boolean;
  points: number;
  bonus?: number;
  scoring: Scoring;
  dict: PlayDictionary;
}) {
  const t = dict.participant.play.questions;
  // ICPC awards no points (`points_awarded` is always 0), so none are
  // announced. A bonus awards none itself either: it gives some back.
  const correctText =
    scoring === "icpc"
      ? t.correctIcpc
      : bonus !== undefined
        ? t.correctBonus.replace("{n}", String(bonus))
        : t.correct.replace("{n}", String(points));
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
 * A question's state in one word. A question closed without a correct answer
 * is "attempts spent", never "accepted".
 */
function QuestionStatus({
  closed,
  correct,
  current,
  t,
}: {
  closed: boolean;
  correct?: boolean;
  current: boolean;
  t: PlayDictionary["participant"]["play"]["questions"];
}) {
  if (closed) {
    return correct ? <Tag tone="good">{t.status.accepted}</Tag> : <Tag tone="mute">{t.status.spent}</Tag>;
  }
  return current ? <Tag tone="ink">{t.status.current}</Tag> : <Tag tone="mute">{t.status.open}</Tag>;
}
