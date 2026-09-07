"use client";

import { useRouter } from "next/navigation";
import { useEffect, useRef, useState } from "react";

import { Tag } from "@/components/ui/tag";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { useContestEvents } from "./use-contest-events";

/**
 * The title and the clock, together, the one thing on this screen that never
 * scrolls out of view.
 *
 * Everything else here reads at the participant's own pace — the story once,
 * the console however long a query takes to write — but the clock is the one
 * fact that changes on its own and is worth seeing without scrolling back up
 * for it, which is why it is `sticky` rather than sitting in the page flow
 * with the rest of the heading.
 *
 * `top-12` clears the product shell's own bar (`h-12`, `AppBar`'s own doc),
 * and `z-10` keeps this one under it rather than over it where the two ever
 * overlap during a scroll.
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
  const { offsetRef, deadlineRef, phase } = useContestEvents(
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
    <div className="sticky top-12 z-10 -mx-10 flex flex-wrap items-center justify-between gap-3 border-b border-line bg-bg px-10 py-3 max-narrow:-mx-4.5 max-narrow:px-4.5">
      <div className="flex min-w-0 items-center gap-3">
        <h1 className="truncate text-row text-ink">{title}</h1>
        {phase === "finished" ? <Tag tone="mute">{t.finishedTag}</Tag> : null}
      </div>
      <PlayClock offsetRef={offsetRef} deadlineRef={deadlineRef} phase={phase} dict={dict} />
    </div>
  );
}

/** What the countdown needs to compute a display: a moment, and the offset and deadline as of that moment. */
type ClockSnapshot = { now: number; offset: number; deadline: number | null };

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
  deadlineRef: React.RefObject<number | null>;
  phase: "waiting" | "running" | "finished";
  dict: Dictionary;
}) {
  const t = dict.participant.play.clock;
  const [snapshot, setSnapshot] = useState<ClockSnapshot>({ now: 0, offset: 0, deadline: null });

  useEffect(() => {
    const tick = () => setSnapshot({ now: Date.now(), offset: offsetRef.current, deadline: deadlineRef.current });
    tick();
    const id = setInterval(tick, 1000);
    return () => clearInterval(id);
    // offsetRef and deadlineRef are refs: stable for the component's whole
    // life, and read through `.current` inside `tick` rather than captured
    // here, so they need no place in this list to stay current.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  if (phase === "waiting") {
    return <ClockText tone="ink-2">{t.waiting}</ClockText>;
  }

  // The channel itself said the contest is over — a fact, not a guess this
  // clock made by reaching zero (see the countdown branch below for why
  // those two are deliberately not the same thing). Checked ahead of the
  // deadline math so a participant who never started still reads "time is
  // up" rather than "starts with your first action" once it is finished.
  if (phase === "finished") {
    return <ClockText tone="bad">{t.timeUp}</ClockText>;
  }

  const { deadline } = snapshot;
  if (deadline === null) {
    // Running, but this participant's own timer has not started — the
    // individual-timing case where the deadline arrives with their first
    // action, not with the contest's own start (Deadline's own doc on the Go
    // side). Showing a blank clock here would read as a bug; this says why.
    return <ClockText tone="ink-2">{t.notStarted}</ClockText>;
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
    return <ClockText tone="bad">{t.timeUp}</ClockText>;
  }

  return (
    <ClockText tone={remainingMs <= 5 * 60_000 ? "bad" : remainingMs <= 15 * 60_000 ? "warn" : "ink"}>
      {formatRemaining(remainingMs)}
    </ClockText>
  );
}

function ClockText({ tone, children }: { tone: "ink" | "ink-2" | "warn" | "bad"; children: React.ReactNode }) {
  return (
    <span
      role="timer"
      aria-live="off"
      className={cn(
        "shrink-0 font-mono text-row tabular-nums",
        tone === "ink" && "text-ink",
        tone === "ink-2" && "text-ink-2",
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
