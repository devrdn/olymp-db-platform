"use client";

import { useRouter } from "next/navigation";
import { useEffect, useRef, useState } from "react";

import { Tag } from "@/components/ui/tag";
import type { PlayDictionary } from "./dictionary";
import { readableDuration } from "@/lib/format/bytes";
import { cn } from "@/lib/utils";

import { useContentLoaded, useRenderedRefusal } from "./content-loaded";
import { PanelToggles } from "./panel-toggles";
import { refusalKind } from "./refusals";
import { useContestEvents } from "./use-contest-events";
import { messageForCode } from "@/lib/i18n/errors";

/**
 * The thin bar above the workspace: the contest's title and the clock. It is
 * the only place that opens the events channel, and the countdown is a leaf
 * (PlayClock), so a tick re-renders nothing else.
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
   * True when the server rendered this page before the contest started: the
   * only case where a start means the page must be fetched again.
   */
  waitingForStart: boolean;
  dict: PlayDictionary;
}) {
  const t = dict.participant.play;
  const router = useRouter();
  // Whether the page below shows "not open now" in the workspace's place
  // (content-loaded.tsx). Only that refusal can go stale under a running
  // channel.
  const renderedRefusal = useRenderedRefusal();
  const renderedDormant = renderedRefusal !== null && refusalKind(renderedRefusal) === "dormant";
  const { offsetRef, deadlineRef, phase, channelError, resync, reopened } = useContestEvents(
    contestId,
    waitingForStart ? "waiting" : "running",
    renderedDormant,
  );

  // Under individual timing the content reads start the clock on the server,
  // possibly after the first sync reported no deadline. Once they succeed,
  // one fresh sync brings the deadline (the hook bounds it to one). Skipped
  // when a deadline is already known.
  const contentLoaded = useContentLoaded();
  useEffect(() => {
    if (contentLoaded && phase === "running" && typeof deadlineRef.current !== "number") resync();
  }, [contentLoaded, phase, resync, deadlineRef]);

  // Refreshed once, on one of two transitions: a waiting room learning the
  // contest started, or a page showing "not open now" learning the
  // participant's window opened (`reopened`). Nothing but a refresh replaces
  // that refusal with the workspace. Other phase changes need no refetch:
  // the content already loaded is still right, and refusals say what changed
  // when an action is attempted.
  const refreshed = useRef(false);
  useEffect(() => {
    const started = waitingForStart && phase === "running";
    const opened = !waitingForStart && reopened;
    if ((started || opened) && !refreshed.current) {
      refreshed.current = true;
      router.refresh();
    }
  }, [waitingForStart, phase, reopened, router]);

  return (
    /* `print:hidden`: a print is the story, never the chrome. */
    <div className="flex shrink-0 flex-wrap items-center justify-between gap-3 border-b border-line bg-bg px-4 py-2.5 print:hidden">
      <div className="flex min-w-0 items-center gap-3">
        {/* One line with an ellipsis where the row is shared; below `narrow` the
            clock wraps to its own line, so the title wraps instead of losing its
            end to an ellipsis. */}
        <div className="flex min-w-0 flex-col">
          <div className="flex min-w-0 items-center gap-3">
            <h1 className="truncate text-row text-ink max-narrow:whitespace-normal">{title}</h1>
            {phase === "finished" ? <Tag tone="mute">{t.finishedTag}</Tag> : null}
          </div>
          {/* The participant is told the organiser sees what they do here
              (docs/ARCHITECTURE.md §9.4). A standing fact, so not a dismissible banner. */}
          <p className="text-small text-ink-2">{t.observed}</p>
        </div>
      </div>
      {/* The clock and the panel toggles, one flex row so they wrap together
          and `justify-between` has two things to push apart. `PanelToggles`
          draws nothing without a workspace below (panel-toggles.tsx). */}
      <div className="flex shrink-0 items-center gap-3">
        <PlayClock
          offsetRef={offsetRef}
          deadlineRef={deadlineRef}
          phase={phase}
          contentLoaded={contentLoaded}
          dict={dict}
        />
        <PanelToggles dict={dict} />
      </div>
      {/* A channel the browser gave up on never retries by itself
          (use-contest-events.ts); without this the clock would just stop.
          `basis-full` puts it on its own line. */}
      {channelError ? (
        <p role="status" aria-live="polite" className="w-full basis-full text-small text-warn">
          {messageForCode(channelError, dict.errors)}
        </p>
      ) : null}
    </div>
  );
}

/** A moment, and the offset and deadline as of that moment. */
type ClockSnapshot = { now: number; offset: number; deadline: number | null | undefined };

/**
 * The countdown. `Date.now()` and the refs are read in the effect, never in
 * render, which only reads the `snapshot` captured once a second; that keeps
 * render pure and costs no extra render. The tick stops at this component:
 * nothing above subscribes to the refs.
 */
function PlayClock({
  offsetRef,
  deadlineRef,
  phase,
  contentLoaded,
  dict,
}: {
  offsetRef: React.RefObject<number>;
  deadlineRef: React.RefObject<number | null | undefined>;
  phase: "waiting" | "running" | "finished";
  /** The workspace's content reads have succeeded (content-loaded.tsx). */
  contentLoaded: boolean;
  dict: PlayDictionary;
}) {
  const t = dict.participant.play.clock;
  const [snapshot, setSnapshot] = useState<ClockSnapshot>({ now: 0, offset: 0, deadline: undefined });

  useEffect(() => {
    // Only the running branch reads `snapshot`; the first `tick()` catches up
    // the moment `phase` becomes "running".
    if (phase !== "running") return;

    const tick = () => setSnapshot({ now: Date.now(), offset: offsetRef.current, deadline: deadlineRef.current });
    tick();
    const id = setInterval(tick, 1000);
    return () => clearInterval(id);
    // Refs are stable and read through `.current` inside `tick`.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [phase]);

  // What a screen reader is told unasked. `role="timer"` is
  // `aria-live="off"`, since announcing every second would be two hours of
  // chatter; instead five minutes left and time up are announced once each,
  // matching the visible tone changes.
  const milestone = clockMilestone(phase, snapshot);
  // State, not a ref: it is compared during render, where a ref's `.current`
  // may not be read.
  const [seenMilestone, setSeenMilestone] = useState<Milestone>("none");
  const [announcement, setAnnouncement] = useState("");
  if (milestone !== seenMilestone) {
    setSeenMilestone(milestone);
    setAnnouncement(milestone === "five" ? t.fiveMinutesLeft : milestone === "timeup" ? t.timeUp : "");
  }
  // sr-only: the visible clock already shows all of this.
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

  // The channel said the contest is over, which is a fact rather than the
  // countdown reaching zero. Checked before the deadline math so a
  // participant who never started reads "time is up".
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
    // No sync yet: say so rather than guess how the contest is timed.
    return (
      <>
        {live}
        <ClockText tone="ink-3">{t.syncing}</ClockText>
      </>
    );
  }
  if (deadline === null && contentLoaded) {
    // The content reads that start an individual clock have succeeded since
    // the sync that said "no deadline"; a fresh sync has been requested.
    return (
      <>
        {live}
        <ClockText tone="ink-3">{t.syncing}</ClockText>
      </>
    );
  }
  if (deadline === null) {
    // Only an individual-timing participant who has not started has no
    // deadline; it arrives with their first read of the contest.
    return (
      <>
        {live}
        <ClockText tone="ink-2">{t.notStarted}</ClockText>
      </>
    );
  }

  const remainingMs = deadline - (snapshot.now + snapshot.offset);
  if (remainingMs <= 0) {
    // Zero here is not `phase` "finished": the shown deadline excludes the
    // server's grace period (sendSync on the Go side), so a submission just
    // after zero may still be accepted. Nothing is disabled; the API refuses
    // if it must.
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
        {readableDuration(Math.floor(remainingMs / 1000))}
      </ClockText>
    </>
  );
}

/** The moments PlayClock's live region interrupts a screen reader for. */
type Milestone = "none" | "five" | "timeup";

/** The milestone the countdown is at, for the live region. */
function clockMilestone(phase: "waiting" | "running" | "finished", snapshot: ClockSnapshot): Milestone {
  // `null` and `undefined` alike: no deadline to be near.
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
