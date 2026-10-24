"use client";

import { Suspense, lazy, useCallback, useEffect, useId, useMemo, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { Tag } from "@/components/ui/tag";
import {
  MAX_OPEN_QUESTIONS,
  QUESTION_MAX_CHARS,
  latestStamp,
  type FeedItem,
  type SeenAnnouncement,
  type SeenClarification,
} from "@/lib/api/clarifications";
import { formatTime } from "@/lib/format/datetime";
import type { Locale } from "@/lib/i18n/config";
import { messageForCode } from "@/lib/i18n/errors";
import { cn } from "@/lib/utils";

import { useClarificationSignal } from "./clarification-signal";
import type { PlayDictionary } from "./dictionary";
import type { QuestionEntry } from "./questions-panel";
import { refusalKind } from "./refusals";
import { askOrganisers, fetchClarifications, type ClarificationsResult } from "./requests";

// Loaded once there is an answer or an announcement to show.
const LazyClarificationText = lazy(() =>
  import("./clarification-text").then((m) => ({ default: m.ClarificationText })),
);

/**
 * How far a refetch after a hint is spread: an announcement reaches every
 * open screen of the contest at once, and five hundred reads in the same
 * instant would queue behind each other in the database.
 */
const HINT_SPREAD_MS = 3_000;

/**
 * A connection's first sync counts as a hint, since one published while the
 * channel was away is lost; within this long after a read it changes nothing,
 * which spares the read every page load would otherwise spend twice.
 */
const RECONNECT_FRESH_MS = 15_000;

type Load = { kind: "loading" } | { kind: "ready" } | { kind: "failed"; code: string };

/**
 * The tab of questions to the organisers (docs/ARCHITECTURE.md §6.5): a form
 * to ask, then the announcements and the questions with their answers.
 *
 * What it shows comes from the server, which sends only the participant's own
 * questions, the public answers and the announcements, and never who asked.
 * A participant's text is shown as typed, line breaks kept; the organisers'
 * answers and announcements as Markdown through `StoryText`.
 *
 * The feed is read when the tab mounts, a moment after each hint from the
 * events channel, and when the channel reconnects. Once the contest is over
 * for the participant the form goes and the list stays as last read: the
 * server refuses every read and write from then on.
 *
 * `onUnreadChange` says whether something arrived that the participant has
 * not seen with the tab open. What was seen is remembered per account and
 * contest in this browser, so a reload does not raise the marker again.
 */
export function ClarificationsPanel({
  contestId,
  accountId,
  questionEntries,
  active,
  onUnreadChange,
  dict,
  locale,
}: {
  contestId: string;
  /** Whose screen this is; what they have seen is kept under it. Null keeps it in memory. */
  accountId: string | null;
  /** The contest's questions as the page shows them; a locked one is never offered. */
  questionEntries: QuestionEntry[];
  /** Whether the tab is showing, which is what marks its entries seen. */
  active: boolean;
  onUnreadChange: (unread: boolean) => void;
  dict: PlayDictionary;
  locale: Locale;
}) {
  const t = dict.participant.play.clarifications;
  const signal = useClarificationSignal();

  const [items, setItems] = useState<FeedItem[]>([]);
  const [next, setNext] = useState<string | null>(null);
  const [open, setOpen] = useState(0);
  const [load, setLoad] = useState<Load>({ kind: "loading" });
  const [refreshFailed, setRefreshFailed] = useState<string | null>(null);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [refusedClosed, setRefusedClosed] = useState(false);
  const closed = refusedClosed || signal.finished;

  const olderLoaded = useRef(false);
  // The newest state for `refresh`, which outlives the render it was made in.
  const itemsRef = useRef(items);
  useEffect(() => {
    itemsRef.current = items;
  });
  const lastRead = useRef(0);
  const inFlight = useRef(false);
  const again = useRef(false);

  const onRefusal = useCallback((code: string) => {
    const kind = refusalKind(code);
    if (kind === "closed" || kind === "excluded") setRefusedClosed(true);
  }, []);

  const refresh = useCallback(async () => {
    if (inFlight.current) {
      again.current = true;
      return;
    }
    inFlight.current = true;
    try {
      const result = await readAgain(contestId, olderLoaded.current ? itemsRef.current.length : 0);
      if (result.kind === "ok") {
        lastRead.current = Date.now();
        setItems(result.feed.items);
        setNext(result.feed.next);
        setOpen(result.feed.open);
        setLoad({ kind: "ready" });
        setRefreshFailed(null);
      } else {
        onRefusal(result.code);
        setLoad((current) => (current.kind === "ready" ? current : { kind: "failed", code: result.code }));
        setRefreshFailed(result.code);
      }
    } finally {
      inFlight.current = false;
      if (again.current) {
        again.current = false;
        void refresh();
      }
    }
  }, [contestId, onRefusal]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // A hint from the channel: read again shortly, at a random moment. A hint
  // while that read is pending joins it rather than pushing it back.
  const seenHints = useRef(signal.hints);
  const hintTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    if (signal.hints === seenHints.current) return;
    seenHints.current = signal.hints;
    if (hintTimer.current !== null) return;
    hintTimer.current = setTimeout(() => {
      hintTimer.current = null;
      void refresh();
    }, Math.random() * HINT_SPREAD_MS);
  }, [signal.hints, refresh]);
  useEffect(
    () => () => {
      if (hintTimer.current !== null) clearTimeout(hintTimer.current);
    },
    [],
  );

  const seenConnections = useRef(signal.connections);
  useEffect(() => {
    if (signal.connections === seenConnections.current) return;
    seenConnections.current = signal.connections;
    if (Date.now() - lastRead.current < RECONNECT_FRESH_MS) return;
    void refresh();
  }, [signal.connections, refresh]);

  const loadOlder = async () => {
    if (next === null) return;
    setLoadingOlder(true);
    const result = await fetchClarifications(contestId, next);
    setLoadingOlder(false);
    if (result.kind === "ok") {
      olderLoaded.current = true;
      setItems((current) => {
        const present = new Set(current.map((item) => item.id));
        return [...current, ...result.feed.items.filter((item) => !present.has(item.id))];
      });
      setNext(result.feed.next);
    } else {
      onRefusal(result.code);
      setRefreshFailed(result.code);
    }
  };

  // The newest stamp seen with the tab open. Read once from this browser: on
  // the server it is empty, which changes no markup, since the list is still
  // loading there and the marker is the side panel's.
  const seenKey = accountId ? `dbc.clarifications.seen.${accountId}.${contestId}` : null;
  const [seen, setSeen] = useState(() => readSeen(seenKey));
  const latest = useMemo(() => latestStamp(items), [items]);
  if (active && latest > seen) setSeen(latest);
  useEffect(() => {
    if (seen !== "") writeSeen(seenKey, seen);
  }, [seenKey, seen]);
  const unread = !active && latest > seen;
  useEffect(() => {
    onUnreadChange(unread);
  }, [unread, onUnreadChange]);

  const questions = useMemo(() => questionLabels(questionEntries, t), [questionEntries, t]);
  const announcements = items.filter((item): item is SeenAnnouncement => item.kind === "announcement");
  const thread = items.filter((item): item is SeenClarification => item.kind === "clarification");

  return (
    <div className="flex flex-col gap-6">
      <p className="text-body text-ink-2">{t.intro}</p>
      {closed ? (
        <p className="text-body text-ink">{t.closed}</p>
      ) : (
        <AskForm
          contestId={contestId}
          questions={questions}
          open={open}
          onAsked={(asked) => {
            setItems((current) => [asked, ...current.filter((item) => item.id !== asked.id)]);
            setOpen((n) => n + 1);
            void refresh();
          }}
          onRefusal={onRefusal}
          dict={dict}
        />
      )}

      {load.kind === "loading" ? <p className="text-body text-ink-2">{t.loading}</p> : null}
      {load.kind === "failed" ? (
        <div className="flex flex-col items-start gap-2">
          <p className="text-body text-ink">{t.failed}</p>
          <p className="text-small text-ink-2">{messageForCode(load.code, dict.errors)}</p>
          {!closed ? (
            <Button variant="secondary" size="sm" onClick={() => void refresh()}>
              {t.retry}
            </Button>
          ) : null}
        </div>
      ) : null}

      {load.kind === "ready" ? (
        <>
          {refreshFailed !== null && !closed ? (
            <div role="status" className="flex flex-wrap items-center gap-2 text-small text-ink-2">
              <span>{t.refreshFailed}</span>
              <button
                type="button"
                onClick={() => void refresh()}
                className="text-accent underline underline-offset-4"
              >
                {t.retry}
              </button>
            </div>
          ) : null}
          {announcements.length > 0 ? (
            <Section title={t.announcements}>
              {announcements.map((item) => (
                <article key={item.id} className="flex flex-col gap-2 border-l-2 border-accent pl-3">
                  <p className="text-small text-ink-3">
                    {t.postedAt.replace("{time}", formatTime(item.postedAt, { locale }))}
                  </p>
                  <OrganiserText markdown={item.body} />
                </article>
              ))}
            </Section>
          ) : null}
          <Section title={t.thread}>
            {thread.length === 0 ? <p className="text-body text-ink-2">{t.empty}</p> : null}
            {thread.map((item) => (
              <ClarificationItem key={item.id} item={item} questions={questions} dict={dict} locale={locale} />
            ))}
          </Section>
          {next !== null ? (
            <Button variant="secondary" size="sm" className="self-start" disabled={loadingOlder} onClick={() => void loadOlder()}>
              {loadingOlder ? t.loadingOlder : t.older}
            </Button>
          ) : null}
        </>
      ) : null}
    </div>
  );
}

type Labels = PlayDictionary["participant"]["play"]["clarifications"];

/** The questions a participant may name, by id, with how each reads. */
type QuestionLabel = { id: string; option: string; about: string };

function questionLabels(entries: QuestionEntry[], t: Labels): QuestionLabel[] {
  const labels: QuestionLabel[] = [];
  for (const entry of entries) {
    if (entry.question.locked) continue;
    const n = String(entry.index);
    const title = entry.question.title.trim();
    labels.push({
      id: entry.question.id,
      option: title ? t.questionOptionTitled.replace("{n}", n).replace("{title}", title) : t.questionOption.replace("{n}", n),
      about: title ? t.aboutQuestionTitled.replace("{n}", n).replace("{title}", title) : t.aboutQuestion.replace("{n}", n),
    });
  }
  return labels;
}

function AskForm({
  contestId,
  questions,
  open,
  onAsked,
  onRefusal,
  dict,
}: {
  contestId: string;
  questions: QuestionLabel[];
  open: number;
  onAsked: (asked: SeenClarification) => void;
  onRefusal: (code: string) => void;
  dict: PlayDictionary;
}) {
  const t = dict.participant.play.clarifications;
  const pickerId = useId();
  const fieldId = useId();
  const counterId = useId();
  const hintId = useId();
  const [text, setText] = useState("");
  const [about, setAbout] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [sent, setSent] = useState(false);
  const atCap = open >= MAX_OPEN_QUESTIONS;
  const empty = text.trim() === "";

  const send = async () => {
    if (pending || empty || atCap) return;
    setPending(true);
    setError(null);
    setSent(false);
    const result = await askOrganisers(contestId, text, about || null);
    setPending(false);
    if (result.kind === "ok") {
      setText("");
      setSent(true);
      onAsked(result.asked);
      return;
    }
    onRefusal(result.code);
    setError(result.code);
  };

  return (
    <form
      className="flex flex-col gap-3"
      onSubmit={(event) => {
        event.preventDefault();
        void send();
      }}
    >
      <div className="flex flex-col gap-1.5">
        <label htmlFor={pickerId} className="text-small text-ink-2">
          {t.about}
        </label>
        <select
          id={pickerId}
          value={about}
          onChange={(event) => setAbout(event.currentTarget.value)}
          className={cn(
            "h-(--control-h) w-full min-w-0 rounded-none border border-edge bg-transparent px-3",
            "text-control text-ink transition-colors duration-(--t-input) ease-standard",
            "hover:border-ink-2 focus-visible:border-ink",
          )}
        >
          <option value="">{t.general}</option>
          {questions.map((q) => (
            <option key={q.id} value={q.id}>
              {q.option}
            </option>
          ))}
        </select>
      </div>
      <div className="flex flex-col gap-1.5">
        <label htmlFor={fieldId} className="text-small text-ink-2">
          {t.label}
        </label>
        <textarea
          id={fieldId}
          value={text}
          maxLength={QUESTION_MAX_CHARS}
          rows={4}
          aria-describedby={`${counterId} ${hintId}`}
          onChange={(event) => {
            setText(event.currentTarget.value);
            setSent(false);
          }}
          onKeyDown={(event) => {
            if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) {
              event.preventDefault();
              void send();
            }
          }}
          className={cn(
            "min-h-24 w-full resize-y rounded-none border border-edge bg-transparent px-3 py-2.5",
            "font-sans text-body text-ink placeholder:text-ink-3",
            "transition-colors duration-(--t-input) ease-standard",
            "hover:border-ink-2 focus-visible:border-ink focus-visible:outline-none",
          )}
        />
        <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1 text-small text-ink-2">
          <span id={counterId} className="tabular-nums">
            {t.counter.replace("{n}", String(text.length)).replace("{max}", String(QUESTION_MAX_CHARS))}
          </span>
          <span className="tabular-nums">
            {t.open.replace("{n}", String(open)).replace("{max}", String(MAX_OPEN_QUESTIONS))}
          </span>
        </div>
        <p id={hintId} className="text-small text-ink-3">
          {t.privacy} {t.shortcut}
        </p>
      </div>
      {atCap ? <p className="text-small text-warn">{t.atCap}</p> : null}
      <Button type="submit" className="self-start" disabled={pending || empty || atCap}>
        {pending ? t.sending : t.send}
      </Button>
      {error !== null ? (
        <p role="alert" className="text-small text-bad">
          {messageForCode(error, dict.errors)}
        </p>
      ) : null}
      <p role="status" className={sent ? "text-small text-good" : "sr-only"}>
        {sent ? t.sent : ""}
      </p>
    </form>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  const headingId = useId();
  return (
    <section aria-labelledby={headingId} className="flex flex-col gap-4">
      <h3 id={headingId} className="font-mono text-label uppercase text-ink-3">
        {title}
      </h3>
      {children}
    </section>
  );
}

function ClarificationItem({
  item,
  questions,
  dict,
  locale,
}: {
  item: SeenClarification;
  questions: QuestionLabel[];
  dict: PlayDictionary;
  locale: Locale;
}) {
  const t = dict.participant.play.clarifications;
  const about = item.questionId ? questions.find((q) => q.id === item.questionId)?.about : undefined;
  const answer = item.answer;

  return (
    <article className="flex min-w-0 flex-col gap-2 border-b border-line pb-4 last:border-b-0">
      <div className="flex flex-wrap items-center gap-2">
        {item.mine ? <Tag tone="mute">{t.mine}</Tag> : <Tag tone="live">{t.public}</Tag>}
        {item.mine && answer?.visibility === "public" ? <Tag tone="good">{t.published}</Tag> : null}
        <span className="text-small text-ink-3">{t.askedAt.replace("{time}", formatTime(item.askedAt, { locale }))}</span>
      </div>
      {about ? <p className="text-small text-ink-2">{about}</p> : null}
      <p className="whitespace-pre-wrap break-words text-body text-ink">{item.body}</p>
      {answer === null ? (
        <p className="text-small text-ink-2">{t.waiting}</p>
      ) : (
        <div className="flex min-w-0 flex-col gap-1.5 bg-sunk p-3">
          <p className="flex flex-wrap items-center gap-2 text-small text-ink-3">
            <span>{t.answeredAt.replace("{time}", formatTime(answer.answeredAt, { locale }))}</span>
            {answer.editedAt ? <Tag tone="mute">{t.edited}</Tag> : null}
          </p>
          {answer.declined ? <p className="text-body text-ink">{t.declined}</p> : null}
          {answer.body.trim() !== "" ? <OrganiserText markdown={answer.body} /> : null}
        </div>
      )}
    </article>
  );
}

/** The organisers' Markdown, shown as plain text while the renderer loads. */
function OrganiserText({ markdown }: { markdown: string }) {
  return (
    <div className="min-w-0 break-words">
      <Suspense fallback={<p className="whitespace-pre-wrap break-words text-body text-ink">{markdown}</p>}>
        <LazyClarificationText markdown={markdown} />
      </Suspense>
    </div>
  );
}

/**
 * The newest page, and again every older page already on screen until as many
 * entries are read as were shown. Reading them again rather than keeping them
 * is what takes an answer withdrawn from everyone off every screen.
 */
async function readAgain(contestId: string, shown: number): Promise<ClarificationsResult> {
  const first = await fetchClarifications(contestId);
  if (first.kind !== "ok") return first;
  const items = [...first.feed.items];
  let next = first.feed.next;
  while (next !== null && items.length < shown) {
    const page = await fetchClarifications(contestId, next);
    if (page.kind !== "ok") break;
    const present = new Set(items.map((item) => item.id));
    items.push(...page.feed.items.filter((item) => !present.has(item.id)));
    next = page.feed.next;
  }
  return { kind: "ok", feed: { ...first.feed, items, next } };
}

function readSeen(key: string | null): string {
  if (key === null) return "";
  try {
    return localStorage.getItem(key) ?? "";
  } catch {
    return "";
  }
}

function writeSeen(key: string | null, value: string) {
  if (key === null) return;
  try {
    localStorage.setItem(key, value);
  } catch {
    // A browser that keeps nothing shows the marker again after a reload.
  }
}
