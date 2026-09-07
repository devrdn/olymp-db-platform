import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import type { ContestPhase } from "./use-contest-events";

const refresh = vi.hoisted(() => vi.fn());
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh }) }));

type EventsSnapshot = { offsetRef: { current: number }; deadlineRef: { current: number | null }; phase: ContestPhase };

// The hook itself is tested on its own (use-contest-events.test.ts); this
// stands in for it so the header can be proven against every phase without
// waiting on a real EventSource.
const events = vi.hoisted(() => ({
  current: { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "waiting" } as EventsSnapshot,
}));
vi.mock("./use-contest-events", () => ({ useContestEvents: () => events.current }));

import { PlayHeader } from "./play-header";

beforeEach(() => {
  refresh.mockClear();
});

describe("PlayHeader", () => {
  test("shows the contest's own title", () => {
    events.current = { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "waiting" };
    render(<PlayHeader contestId="c1" title="The Greenhouse Case" waitingForStart dict={en} />);

    expect(screen.getByRole("heading", { name: "The Greenhouse Case" })).toBeInTheDocument();
  });

  test("shows a waiting clock before the contest has started", () => {
    events.current = { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "waiting" };
    render(<PlayHeader contestId="c1" title="X" waitingForStart dict={en} />);

    expect(screen.getByRole("timer")).toHaveTextContent(en.participant.play.clock.waiting);
  });

  test("shows a running participant's own timer counting down", () => {
    events.current = {
      offsetRef: { current: 0 },
      deadlineRef: { current: Date.now() + 90_000 },
      phase: "running",
    };
    render(<PlayHeader contestId="c1" title="X" waitingForStart={false} dict={en} />);

    expect(screen.getByRole("timer")).toHaveTextContent(/1:2\d|1:3\d/);
  });

  test("says a deadline has not started rather than showing a blank clock", () => {
    events.current = { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "running" };
    render(<PlayHeader contestId="c1" title="X" waitingForStart={false} dict={en} />);

    expect(screen.getByRole("timer")).toHaveTextContent(en.participant.play.clock.notStarted);
  });

  test("does not disable anything once the deadline has passed", () => {
    events.current = { offsetRef: { current: 0 }, deadlineRef: { current: Date.now() - 5_000 }, phase: "running" };
    render(<PlayHeader contestId="c1" title="X" waitingForStart={false} dict={en} />);

    // The screen says time is up; it does not decide anything on its own —
    // there is nothing here to disable, only a clock to read honestly.
    expect(screen.getByRole("timer")).toHaveTextContent(en.participant.play.clock.timeUp);
  });

  test("refreshes the page once, the moment a waiting room learns the contest started", () => {
    events.current = { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "running" };
    render(<PlayHeader contestId="c1" title="X" waitingForStart dict={en} />);

    expect(refresh).toHaveBeenCalledTimes(1);
  });

  test("never refreshes a screen that already knows the contest is running", () => {
    events.current = { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "running" };
    render(<PlayHeader contestId="c1" title="X" waitingForStart={false} dict={en} />);

    expect(refresh).not.toHaveBeenCalled();
  });

  test("marks the contest finished once the channel says so", () => {
    events.current = { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "finished" };
    render(<PlayHeader contestId="c1" title="X" waitingForStart={false} dict={en} />);

    expect(screen.getByText(en.participant.play.finishedTag)).toBeInTheDocument();
  });
});
