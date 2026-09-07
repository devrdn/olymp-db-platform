"use client";

import { useRouter } from "next/navigation";
import { useEffect, useRef, useState } from "react";

import { Tag } from "@/components/ui/tag";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { useContestEvents } from "./use-contest-events";

/**
 * The thin bar above the workspace: the contest's title and the clock,
 * together — the one thing on this screen every panel sits below.
 *
 * It used to be `sticky`, compensating for `Band`'s own padding with a
 * negative margin so its border ran edge to edge within the content column
 * (a page that scrolled, with this bar pinned to the top of it). The
 * workspace it sits in now (Task 3) does not scroll as a whole — it is
 * itself exactly one screen tall below the product shell's own bar, with
 * every panel scrolling on its own — so this is simply the fixed first row
 * of that layout, full width already, needing neither.
 *
 * This is also the only place on the page that opens the events channel: the
 * one hook call lives here, and the countdown it drives is a leaf of this
 * component alone (see PlayClock below) — nothing about a participant typing
 * in the console or reading the story is affected by a tick.
 */
export function PlayHeader({
  contestId,
  title,
  waitingForStart,
  dict,
}: {
  contestId: string;
  title: string;
  /**
   * True when the server rendered this page before the contest had started —
   * the only case where "the contest just started" means this screen has
   * nothing loaded yet and has to ask the server for it.
   */
  waitingForStart: boolean;
  dict: Dictionary;
}) {
  const t = dict.participant.play;
  const router = useRouter();
  const { offsetRef, deadlineRef, phase, channelError } = useContestEvents(
    contestId,
    waitingForStart ? "waiting" : "running",
  );

  // Refreshed once, the moment this specific transition matters. Every other
  // phase change (running while already showing the running screen, or
  // finishing) needs no refetch: the story and the questions a participant
  // has already have not become wrong, and the existing refusal flow already
  // says what changed the moment an action is actually attempted.
  const refreshed = useRef(false);
  useEffect(() => {
    if (waitingForStart && phase === "running" && !refreshed.current) {
      refreshed.current = true;
      router.refresh();
    }
  }, [waitingForStart, phase, router]);

  return (
    <div className="flex shrink-0 flex-wrap items-center justify-between gap-3 border-b border-line bg-bg px-4 py-2.5">
      <div className="flex min-w-0 items-center gap-3">
        <h1 className="truncate text-row text-ink">{title}</h1>
        {phase === "finished" ? <Tag tone="mute">{t.finishedTag}</Tag> : null}
      </div>
      <PlayClock offsetRef={offsetRef} deadlineRef={deadlineRef} phase={phase} dict={dict} />
      {/* Finding 1: the channel this clock runs on can fail outright (a
          connection limit, a rate limit, this account losing access) and, per
          the SSE spec, the browser then never retries on its own — see
          use-contest-events.ts's own doc. Without this, that failure was
          invisible: the clock simply stopped moving, with nothing on screen
          to say why. `w-full` forces it onto its own line in this flex-wrap
          row rather than squeezing the title or the clock. */}
      {channelError ? (
        <p role="status" aria-live="polite" className="w-full basis-full text-small text-warn">
          {(dict.errors as Record<string, string>)[channelError] ?? dict.errors.fallback}
        </p>
      ) : null}
    </div>
  );
}

/** What the countdown needs to compute a display: a moment, and the offset and deadline as of that moment. */
type ClockSnapshot = { now: number; offset: number; deadline: number | null | undefined };

/**
 * The countdown itself.
 *
 * `Date.now()` and the two refs are read from inside the effect below, never
 * from the render body: render only ever looks at `snapshot`, a plain object
 * captured once a second. That is what keeps this component pure — the rules
 * of React ask for exactly this, not a style preference — and it costs
 * nothing extra: the effect's own `setState` is what causes the once-a-second
 * render this component already needs, so the snapshot is never a second
 * render for the price of the first.
 *
 * That render stops at this component regardless: `offsetRef` and
 * `deadlineRef` are mutable refs nothing above this subscribes to, and the
 * parent re-renders only on a `phase` change, a handful of times in two
 * hours. The once-a-second cost here is one `Date.now()`, one subtraction and
 * a short string, confined to a single `<span>`.
 */
function PlayClock({
  offsetRef,
  deadlineRef,
  phase,
  dict,
}: {
  offsetRef: React.RefObject<number>;
  deadlineRef: React.RefObject<number | null | undefined>;
  phase: "waiting" | "running" | "finished";
  dict: Dictionary;
}) {
  const t = dict.participant.play.clock;
  const [snapshot, setSnapshot] = useState<ClockSnapshot>({ now: 0, offset: 0, deadline: undefined });

  useEffect(() => {
    // Nothing reads `snapshot` outside the running branch below — "waiting"
    // and "finished" are fixed text — so a tick while either of those is
    // showing would cost a render for a `<span>` that never changes (finding
    // 6). The countdown itself catches up in one `tick()` the instant `phase`
    // becomes "running", so nothing is lost by not ticking before then.
    if (phase !== "running") return;

    const tick = () => setSnapshot({ now: Date.now(), offset: offsetRef.current, deadline: deadlineRef.current });
    tick();
    const id = setInterval(tick, 1000);
    return () => clearInterval(id);
    // offsetRef and deadlineRef are refs: stable for the component's whole
    // life, and read through `.current` inside `tick` rather than captured
    // here, so they need no place in this list to stay current.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [phase]);

  // What a screen reader is told without being asked, and when (finding 7).
  // `role="timer"` below is `aria-live="off"`: reading the whole countdown
  // out loud every second would turn a two-hour contest into two hours of
  // chatter, so nothing is announced by default. But a participant who
  // cannot glance at a sticky corner of the screen still needs to know the
  // deadline is close, the same way the sighted tone changes below already
  // say it in color — bad at five minutes, warn at fifteen. Five minutes is
  // the threshold chosen to interrupt for: the earlier, fifteen-minute color
  // change is a nudge a glance already covers, but five minutes is close
  // enough that missing it matters, and late enough that only one
  // interruption is ever owed for it. "Time is up" is the other: the
  // countdown reaching zero, once, the same milestone the visible clock
  // marks by turning "bad" for the second time. Computed once per render
  // from `phase` and `snapshot` rather than duplicated across the branches
  // below, so every path — including a deadline that resolves to already
  // expired — shares the one place that decides whether this render just
  // crossed a threshold.
  const milestone = clockMilestone(phase, snapshot);
  // State, not a ref: the comparison below runs during render (the same
  // "adjust state when something changes" pattern questions-panel.tsx's own
  // formKey logic uses), and a ref's `.current` may not be read there — only
  // state may.
  const [seenMilestone, setSeenMilestone] = useState<Milestone>("none");
  const [announcement, setAnnouncement] = useState("");
  if (milestone !== seenMilestone) {
    setSeenMilestone(milestone);
    setAnnouncement(milestone === "five" ? t.fiveMinutesLeft : milestone === "timeup" ? t.timeUp : "");
  }
  // sr-only: present for assistive technology, invisible otherwise — the
  // sighted clock beside it already shows every one of these facts in color
  // and text, continuously, so this exists only for the reader that cannot
  // see it.
  const live = (
    <span aria-live="polite" className="sr-only">
      {announcement}
    </span>
  );

  if (phase === "waiting") {
    return (
      <>
        {live}
        <ClockText tone="ink-2">{t.waiting}</ClockText>
      </>
    );
  }

  // The channel itself said the contest is over — a fact, not a guess this
  // clock made by reaching zero (see the countdown branch below for why
  // those two are deliberately not the same thing). Checked ahead of the
  // deadline math so a participant who never started still reads "time is
  // up" rather than "starts with your first action" once it is finished.
  if (phase === "finished") {
    return (
      <>
        {live}
        <ClockText tone="bad">{t.timeUp}</ClockText>
      </>
    );
  }

  const { deadline } = snapshot;
  if (deadline === undefined) {
    // No sync has arrived yet. The clock is a fact the server owns, and the
    // honest thing to show while waiting for it is that we are waiting —
    // not a claim about how this contest is timed.
    return (
      <>
        {live}
        <ClockText tone="ink-3">{t.syncing}</ClockText>
      </>
    );
  }
  if (deadline === null) {
    // A sync arrived and carried no deadline, which the server only does for
    // an individual-timing participant who has not started: their deadline
    // arrives with their first action, not with the contest's own start
    // (Deadline's own doc on the Go side).
    return (
      <>
        {live}
        <ClockText tone="ink-2">{t.notStarted}</ClockText>
      </>
    );
  }

  const remainingMs = deadline - (snapshot.now + snapshot.offset);
  if (remainingMs <= 0) {
    // The countdown reaching zero is not the same fact as `phase` becoming
    // "finished": the deadline shown here deliberately excludes the grace
    // period the server still holds before refusing a late answer
    // (sendSync's own doc on the Go side), so a submission made right after
    // this reads zero can still be accepted. Nothing here disables anything —
    // the API's own refusal, if there is one, is what the console and the
    // answer forms already show.
    return (
      <>
        {live}
        <ClockText tone="bad">{t.timeUp}</ClockText>
      </>
    );
  }

  return (
    <>
      {live}
      <ClockText tone={remainingMs <= 5 * 60_000 ? "bad" : remainingMs <= 15 * 60_000 ? "warn" : "ink"}>
        {formatRemaining(remainingMs)}
      </ClockText>
    </>
  );
}

/** The two moments PlayClock's live region ever interrupts a screen reader for, and "none" the rest of the time. */
type Milestone = "none" | "five" | "timeup";

/** What PlayClock's own countdown math would show, reduced to just the milestone the live region cares about (see PlayClock's own doc, finding 7). */
function clockMilestone(phase: "waiting" | "running" | "finished", snapshot: ClockSnapshot): Milestone {
  // `null` and `undefined` alike: there is no deadline to be near.
  if (phase !== "running" || snapshot.deadline == null) return "none";
  const remainingMs = snapshot.deadline - (snapshot.now + snapshot.offset);
  if (remainingMs <= 0) return "timeup";
  if (remainingMs <= 5 * 60_000) return "five";
  return "none";
}

function ClockText({ tone, children }: { tone: "ink" | "ink-2" | "ink-3" | "warn" | "bad"; children: React.ReactNode }) {
  return (
    <span
      role="timer"
      aria-live="off"
      className={cn(
        "shrink-0 font-mono text-row tabular-nums",
        tone === "ink" && "text-ink",
        tone === "ink-2" && "text-ink-2",
        tone === "ink-3" && "text-ink-3",
        tone === "warn" && "text-warn",
        tone === "bad" && "text-bad",
      )}
    >
      {children}
    </span>
  );
}

function formatRemaining(ms: number): string {
  const totalSeconds = Math.floor(ms / 1000);
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  const pad = (n: number) => String(n).padStart(2, "0");
  return hours > 0 ? `${hours}:${pad(minutes)}:${pad(seconds)}` : `${minutes}:${pad(seconds)}`;
}
