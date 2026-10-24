"use client";

import { useRouter } from "next/navigation";
import { useCallback, useEffect, useId, useRef, useState } from "react";

import { StoryText } from "@/components/product/story-text";
import { Button } from "@/components/ui/button";
import { Tag } from "@/components/ui/tag";
import {
  ANNOUNCEMENT_MAX_CHARS,
  ANSWER_MAX_CHARS,
  CLARIFICATION_PAGE_SIZE,
  QUEUE_MAX_SHOWN,
  answerClarification,
  fetchAnnouncements,
  fetchQueue,
  postAnnouncement,
  type AnnouncementList,
  type AnswerInput,
  type ClarificationQueue,
  type QueueState,
  type StaffClarification,
} from "@/lib/api/clarifications";
import { formatTime } from "@/lib/format/datetime";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { messageForCode } from "@/lib/i18n/errors";
import { cn } from "@/lib/utils";

/** How often the desk reads the queue while its tab is visible, as monitoring does. */
export const DESK_POLL_MS = 5_000;

export type DeskDictionary = Pick<Dictionary, "workspace" | "errors">;

/** A question of the contest as the desk names it. */
export type QuestionLabel = { id: string; label: string };

type Problem = "forbidden" | "failed" | null;

/**
 * The staff desk for participants' questions (docs/ARCHITECTURE.md §6.5): the
 * waiting and the answered questions, each with who asked, an answer form, and
 * the announcement composer.
 *
 * It polls the shown state of the queue every five seconds while the tab is
 * visible, not over the events channel, which is the participants'. Each read
 * replaces the list from its start, so a question another organiser answered
 * leaves the waiting list; a form being typed in keeps its text while its
 * question stays. It keeps the first hundred live; older ones are read a page
 * at a time on request, each after the last item held, and are not polled.
 * Outside a running contest the desk only reads: the server
 * refuses every answer and announcement there, since nobody could read them.
 *
 * A participant's text is shown as typed; answers and announcements are
 * Markdown, rendered as participants will see them.
 */
export function ClarificationsDesk({
  contestId,
  running,
  questions,
  initialQueue,
  initialAnnouncements,
  dict,
  locale,
}: {
  contestId: string;
  running: boolean;
  questions: QuestionLabel[];
  /** The waiting questions, as the page read them. */
  initialQueue: ClarificationQueue;
  initialAnnouncements: AnnouncementList;
  dict: DeskDictionary;
  locale: string;
}) {
  const t = dict.workspace.clarifications;
  const [state, setState] = useState<QueueState>("open");
  const [items, setItems] = useState(initialQueue.items);
  const [counts, setCounts] = useState(initialQueue.counts);
  const [shown, setShown] = useState(CLARIFICATION_PAGE_SIZE);
  const [problem, setProblem] = useState<Problem>(null);
  const [notice, setNotice] = useState<string | null>(null);
  // Pages read past the live hundred; not polled, kept until the state changes.
  const [further, setFurther] = useState<StaffClarification[]>([]);
  const noticeRef = useRef<HTMLParagraphElement>(null);

  // What a poll must read, current for the chain, which outlives renders.
  const wanted = useRef({ state, shown });
  useEffect(() => {
    wanted.current = { state, shown };
  });
  // Bumped when the state changes, so an answer to an earlier read is dropped.
  const generation = useRef(0);

  // The navigation's waiting count is drawn once by the server layout, so a
  // change the desk sees redraws it; a poll that changes nothing costs nothing.
  const router = useRouter();
  const drawnOpen = useRef(initialQueue.counts.open);
  useEffect(() => {
    if (counts.open === drawnOpen.current) return;
    drawnOpen.current = counts.open;
    router.refresh();
  }, [counts.open, router]);

  const read = useCallback(async () => {
    const { state: reading, shown: limit } = wanted.current;
    const mine = generation.current;
    const result = await fetchQueue(contestId, reading, { limit });
    if (mine !== generation.current) return true;
    if (result.kind === "ok") {
      setItems(result.queue.items);
      setCounts(result.queue.counts);
      setProblem(null);
      return true;
    }
    if (result.code === "forbidden" || result.code === "not_found") {
      setProblem("forbidden");
      return false;
    }
    setProblem("failed");
    return true;
  }, [contestId]);

  // One chain of polls, each scheduled after the last answer, while visible.
  useEffect(() => {
    if (!running) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const schedule = () => {
      if (stopped) return;
      timer = setTimeout(tick, DESK_POLL_MS);
    };
    const tick = async () => {
      timer = null;
      if (document.visibilityState !== "visible") return schedule();
      if (await read()) schedule();
      else stopped = true;
    };
    const onVisible = () => {
      if (document.visibilityState !== "visible" || timer === null || stopped) return;
      clearTimeout(timer);
      void tick();
    };
    schedule();
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      stopped = true;
      if (timer !== null) clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [running, read]);

  const choose = (next: QueueState) => {
    if (next === state) return;
    generation.current++;
    wanted.current = { state: next, shown: CLARIFICATION_PAGE_SIZE };
    setState(next);
    setShown(CLARIFICATION_PAGE_SIZE);
    setItems([]);
    setFurther([]);
    void read();
  };

  const showMore = async () => {
    if (shown < QUEUE_MAX_SHOWN) {
      const next = Math.min(shown + CLARIFICATION_PAGE_SIZE, QUEUE_MAX_SHOWN);
      wanted.current = { state, shown: next };
      setShown(next);
      void read();
      return;
    }
    const mine = generation.current;
    const result = await fetchQueue(contestId, state, {
      limit: CLARIFICATION_PAGE_SIZE,
      after: (further.at(-1) ?? items.at(-1))?.id,
    });
    if (mine !== generation.current) return;
    if (result.kind === "ok") {
      setFurther((current) => {
        const held = new Set(current.map((item) => item.id));
        return [...current, ...result.queue.items.filter((item) => !held.has(item.id))];
      });
      setCounts(result.queue.counts);
    } else {
      setProblem("failed");
    }
  };

  const total = state === "open" ? counts.open : counts.answered;
  const live = new Set(items.map((item) => item.id));
  const listed = [...items, ...further.filter((item) => !live.has(item.id))];
  const labelOf = (id: string | null) =>
    id ? (questions.find((q) => q.id === id)?.label ?? null) : t.general;

  return (
    <div className="flex flex-col gap-10">
      <section aria-labelledby={`${contestId}-queue`} className="flex flex-col gap-5">
        <h3 id={`${contestId}-queue`} className="sr-only">
          {dict.workspace.tabs.clarifications}
        </h3>
        {!running ? <p className="max-w-body text-body text-ink">{t.closed}</p> : null}
        <div role="group" aria-label={t.states} className="flex flex-wrap gap-2">
          <Button variant={state === "open" ? "primary" : "secondary"} size="sm" aria-pressed={state === "open"} onClick={() => choose("open")}>
            {t.waiting.replace("{n}", String(counts.open))}
          </Button>
          <Button
            variant={state === "answered" ? "primary" : "secondary"}
            size="sm"
            aria-pressed={state === "answered"}
            onClick={() => choose("answered")}
          >
            {t.answered.replace("{n}", String(counts.answered))}
          </Button>
        </div>
        {problem === "forbidden" ? <p className="text-body text-bad">{t.forbidden}</p> : null}
        {problem === "failed" ? (
          <p role="status" className="text-small text-warn">
            {t.pollFailed}
          </p>
        ) : null}
        {/* Takes focus when an answered question leaves the list under it. */}
        <p ref={noticeRef} tabIndex={-1} role="status" className={notice ? "text-small text-good" : "sr-only"}>
          {notice ?? ""}
        </p>
        {listed.length === 0 ? (
          <p className="text-body text-ink-2">{state === "open" ? t.emptyOpen : t.emptyAnswered}</p>
        ) : null}
        <div className="flex flex-col gap-4">
          {listed.map((item) => (
            <QueueItem
              key={item.id}
              item={item}
              about={labelOf(item.questionId)}
              contestId={contestId}
              running={running}
              dict={dict}
              locale={locale}
              onWritten={(written, mode) => {
                setNotice(t.sent);
                if (mode === "answer") {
                  noticeRef.current?.focus();
                  setItems((current) => current.filter((c) => c.id !== written.id));
                  setCounts((c) => ({ open: Math.max(0, c.open - 1), answered: c.answered + 1 }));
                } else {
                  const replace = (list: StaffClarification[]) => list.map((c) => (c.id === written.id ? written : c));
                  setItems(replace);
                  setFurther(replace);
                }
              }}
            />
          ))}
        </div>
        {listed.length < total ? (
          <Button variant="secondary" size="sm" className="self-start" onClick={() => void showMore()}>
            {t.older}
          </Button>
        ) : null}
      </section>

      <Announcements contestId={contestId} running={running} initial={initialAnnouncements} dict={dict} locale={locale} />
    </div>
  );
}

function QueueItem({
  item,
  about,
  contestId,
  running,
  dict,
  locale,
  onWritten,
}: {
  item: StaffClarification;
  /** What the question is about; null for a question of the contest no longer listed. */
  about: string | null;
  contestId: string;
  running: boolean;
  dict: DeskDictionary;
  locale: string;
  onWritten: (written: StaffClarification, mode: "answer" | "edit") => void;
}) {
  const t = dict.workspace.clarifications;
  const [editing, setEditing] = useState(false);
  const answer = item.answer;
  // Closing the form unmounts the focused control, so focus returns to Edit.
  const editRef = useRef<HTMLButtonElement>(null);
  const refocus = useRef(false);
  useEffect(() => {
    if (!editing && refocus.current) {
      refocus.current = false;
      editRef.current?.focus();
    }
  }, [editing]);
  const close = () => {
    refocus.current = true;
    setEditing(false);
  };

  return (
    <article className="flex min-w-0 flex-col gap-3 border border-line p-4">
      <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <p className="text-row text-ink">
          {t.asker.replace("{name}", item.asker.fullName).replace("{login}", item.asker.login)}
        </p>
        <p className="text-small text-ink-3">{t.askedAt.replace("{time}", formatTime(item.createdAt, { locale }))}</p>
      </div>
      {about ? <p className="text-small text-ink-2">{about}</p> : null}
      <p className="whitespace-pre-wrap break-words text-body text-ink">{item.body}</p>

      {answer !== null ? (
        <div className="flex min-w-0 flex-col gap-2 bg-sunk p-3">
          <div className="flex flex-wrap items-center gap-2 text-small text-ink-3">
            <Tag tone={answer.visibility === "public" ? "live" : "mute"}>{t.visibilityTag[answer.visibility]}</Tag>
            {answer.declined ? <Tag tone="warn">{t.declined}</Tag> : null}
            {answer.editedAt ? <Tag tone="mute">{t.edited}</Tag> : null}
            <span>
              {answer.answeredBy
                ? t.answeredBy.replace("{login}", answer.answeredBy).replace("{time}", formatTime(answer.answeredAt, { locale }))
                : t.answeredAt.replace("{time}", formatTime(answer.answeredAt, { locale }))}
            </span>
          </div>
          {answer.body.trim() !== "" ? (
            <StoryText markdown={answer.body} className="max-w-none font-sans text-body" />
          ) : null}
          {running && !editing ? (
            <Button ref={editRef} variant="quiet" size="sm" className="self-start" onClick={() => setEditing(true)}>
              {t.edit}
            </Button>
          ) : null}
        </div>
      ) : null}

      {running && (answer === null || editing) ? (
        <AnswerForm
          initial={answer ? { body: answer.body, declined: answer.declined, visibility: answer.visibility } : null}
          dict={dict}
          focusOnOpen={answer !== null}
          onCancel={answer ? close : undefined}
          submit={async (input) => {
            const mode = answer ? "edit" : "answer";
            const result = await answerClarification(contestId, item.id, input, mode);
            if (result.kind === "ok") {
              if (mode === "edit") close();
              onWritten(result.clarification, mode);
              return null;
            }
            return result.code;
          }}
        />
      ) : null}
    </article>
  );
}

function AnswerForm({
  initial,
  dict,
  submit,
  onCancel,
  focusOnOpen = false,
}: {
  /** The answer being edited; null for a first answer. */
  initial: AnswerInput | null;
  /** Moves focus to the field when the form opens, as Edit opens it. */
  focusOnOpen?: boolean;
  dict: DeskDictionary;
  /** Sends the answer; resolves to a refusal code, or null once written. */
  submit: (input: AnswerInput) => Promise<string | null>;
  onCancel?: () => void;
}) {
  const t = dict.workspace.clarifications;
  const fieldId = useId();
  const counterId = useId();
  const noteId = useId();
  const visibilityName = useId();
  const [body, setBody] = useState(initial?.body ?? "");
  const [visibility, setVisibility] = useState(initial?.visibility ?? "private");
  const [declined, setDeclined] = useState(initial?.declined ?? false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const ready = declined || body.trim() !== "";
  const fieldRef = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    if (focusOnOpen) fieldRef.current?.focus();
  }, [focusOnOpen]);

  return (
    <form
      className="flex flex-col gap-3"
      onSubmit={async (event) => {
        event.preventDefault();
        if (!ready || pending) return;
        setPending(true);
        setError(null);
        const refused = await submit({ body, declined, visibility });
        setPending(false);
        if (refused !== null) setError(refused);
      }}
    >
      <div className="flex flex-col gap-1.5">
        <label htmlFor={fieldId} className="text-small text-ink-2">
          {t.answerLabel}
        </label>
        <textarea
          ref={fieldRef}
          id={fieldId}
          value={body}
          maxLength={ANSWER_MAX_CHARS}
          rows={4}
          aria-describedby={`${counterId} ${noteId}`}
          onChange={(event) => setBody(event.currentTarget.value)}
          className={cn(
            "min-h-24 w-full resize-y rounded-none border border-edge bg-transparent px-3 py-2.5",
            "font-sans text-body text-ink placeholder:text-ink-3",
            "transition-colors duration-(--t-input) ease-standard",
            "hover:border-ink-2 focus-visible:border-ink focus-visible:outline-none",
          )}
        />
        <div className="flex flex-wrap justify-between gap-x-3 text-small text-ink-3">
          <span id={noteId}>{t.markdownNote}</span>
          <span id={counterId} className="tabular-nums">
            {t.counter.replace("{n}", String(body.length)).replace("{max}", String(ANSWER_MAX_CHARS))}
          </span>
        </div>
      </div>
      <fieldset className="flex flex-col gap-1.5">
        <legend className="text-small text-ink-2">{t.visibility}</legend>
        <label className="flex items-center gap-2 text-body text-ink">
          <input
            type="radio"
            name={visibilityName}
            checked={visibility === "private"}
            onChange={() => setVisibility("private")}
          />
          {t.private}
        </label>
        <label className="flex items-center gap-2 text-body text-ink">
          <input
            type="radio"
            name={visibilityName}
            checked={visibility === "public"}
            onChange={() => setVisibility("public")}
          />
          {t.public}
        </label>
        <p className={cn("text-small", visibility === "public" ? "text-warn" : "text-ink-3")}>{t.publicNote}</p>
      </fieldset>
      <div className="flex flex-col gap-1">
        <label className="flex items-center gap-2 text-body text-ink">
          <input type="checkbox" checked={declined} onChange={(event) => setDeclined(event.currentTarget.checked)} />
          {t.decline}
        </label>
        <p className="text-small text-ink-3">{t.declineNote}</p>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button type="submit" size="sm" disabled={!ready || pending}>
          {pending ? t.sending : initial ? t.save : t.send}
        </Button>
        {onCancel ? (
          <Button type="button" variant="quiet" size="sm" onClick={onCancel}>
            {t.cancel}
          </Button>
        ) : null}
      </div>
      {error !== null ? (
        <p role="alert" className="text-small text-bad">
          {messageForCode(error, dict.errors)}
        </p>
      ) : null}
    </form>
  );
}

function Announcements({
  contestId,
  running,
  initial,
  dict,
  locale,
}: {
  contestId: string;
  running: boolean;
  initial: AnnouncementList;
  dict: DeskDictionary;
  locale: string;
}) {
  const t = dict.workspace.clarifications;
  const headingId = useId();
  const fieldId = useId();
  const counterId = useId();
  const [items, setItems] = useState(initial.items);
  const [total, setTotal] = useState(initial.total);
  const [body, setBody] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [posted, setPosted] = useState(false);

  const loadOlder = async () => {
    const result = await fetchAnnouncements(contestId, items.length);
    if (result.kind === "ok") {
      setItems((current) => {
        const present = new Set(current.map((a) => a.id));
        return [...current, ...result.list.items.filter((a) => !present.has(a.id))];
      });
      setTotal(result.list.total);
    } else {
      setError(result.code);
    }
  };

  return (
    <section aria-labelledby={headingId} className="flex flex-col gap-5">
      <h3 id={headingId} className="text-h3 text-ink">
        {t.announcements}
      </h3>
      {running ? (
        <form
          className="flex flex-col gap-3"
          onSubmit={async (event) => {
            event.preventDefault();
            if (pending || body.trim() === "") return;
            setPending(true);
            setError(null);
            setPosted(false);
            const result = await postAnnouncement(contestId, body);
            setPending(false);
            if (result.kind === "ok") {
              setItems((current) => [result.announcement, ...current]);
              setTotal((n) => n + 1);
              setBody("");
              setPosted(true);
            } else {
              setError(result.code);
            }
          }}
        >
          <div className="flex flex-col gap-1.5">
            <label htmlFor={fieldId} className="text-small text-ink-2">
              {t.announcementLabel}
            </label>
            <textarea
              id={fieldId}
              value={body}
              maxLength={ANNOUNCEMENT_MAX_CHARS}
              rows={3}
              aria-describedby={counterId}
              onChange={(event) => {
                setBody(event.currentTarget.value);
                setPosted(false);
              }}
              className={cn(
                "min-h-20 w-full resize-y rounded-none border border-edge bg-transparent px-3 py-2.5",
                "font-sans text-body text-ink placeholder:text-ink-3",
                "transition-colors duration-(--t-input) ease-standard",
                "hover:border-ink-2 focus-visible:border-ink focus-visible:outline-none",
              )}
            />
            <div className="flex flex-wrap justify-between gap-x-3 text-small text-ink-3">
              <span>
                {t.announcementNote} {t.markdownNote}
              </span>
              <span id={counterId} className="tabular-nums">
                {t.counter.replace("{n}", String(body.length)).replace("{max}", String(ANNOUNCEMENT_MAX_CHARS))}
              </span>
            </div>
          </div>
          <Button type="submit" size="sm" className="self-start" disabled={pending || body.trim() === ""}>
            {pending ? t.posting : t.post}
          </Button>
          {error !== null ? (
            <p role="alert" className="text-small text-bad">
              {messageForCode(error, dict.errors)}
            </p>
          ) : null}
          <p role="status" className={posted ? "text-small text-good" : "sr-only"}>
            {posted ? t.posted : ""}
          </p>
        </form>
      ) : null}
      {items.length === 0 ? <p className="text-body text-ink-2">{t.noAnnouncements}</p> : null}
      <div className="flex flex-col gap-4">
        {items.map((a) => (
          <div key={a.id} className="flex min-w-0 flex-col gap-1.5 border-l-2 border-accent pl-3">
            <p className="text-small text-ink-3">{t.postedAt.replace("{time}", formatTime(a.createdAt, { locale }))}</p>
            <StoryText markdown={a.body} className="max-w-none font-sans text-body" />
          </div>
        ))}
      </div>
      {items.length < total ? (
        <Button variant="secondary" size="sm" className="self-start" onClick={() => void loadOlder()}>
          {t.older}
        </Button>
      ) : null}
    </section>
  );
}
