import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import type { ContestPhase } from "./use-contest-events";

const refresh = vi.hoisted(() => vi.fn());
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh }) }));

type EventsSnapshot = {
  offsetRef: { current: number };
  deadlineRef: { current: number | null | undefined };
  phase: ContestPhase;
  channelError?: string | null;
  resync?: () => void;
};

// The hook itself is tested on its own (use-contest-events.test.ts); this
// stands in for it so the header can be proven against every phase without
// waiting on a real EventSource.
const events = vi.hoisted(() => ({
  current: { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "waiting" } as EventsSnapshot,
}));
vi.mock("./use-contest-events", () => ({ useContestEvents: () => events.current }));

import { ContentLoadedProvider, ContentLoadedSignal } from "./content-loaded";
import { PanelVisibilityProvider } from "./panel-toggles";
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

  // The title is one line with an ellipsis where it shares the bar with the
  // clock, and wraps where it does not. Measured at 375px, where the clock
  // has already wrapped onto its own line: the title lost its last 75px with
  // an empty second line underneath it. jsdom cannot measure that; what it
  // can hold is that both rules are still declared, and for which range.
  test("the title truncates on a shared row and wraps on the narrow fallback", () => {
    events.current = { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "waiting" };
    render(<PlayHeader contestId="c1" title="The Greenhouse Case" waitingForStart dict={en} />);

    const title = screen.getByRole("heading", { level: 1 });
    expect(title.className).toMatch(/(^|\s)truncate(\s|$)/);
    expect(title.className).toMatch(/(^|\s)max-narrow:whitespace-normal(\s|$)/);
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

  // The bug this separates two states to prevent. Before the events channel
  // has synced once, nothing on the client knows this participant's deadline
  // — and a fixed-window contest, which is most of them, was being told its
  // countdown starts with the participant's first action. Not early: false.
  test("says it is synchronising rather than claiming how the contest is timed", () => {
    events.current = { offsetRef: { current: 0 }, deadlineRef: { current: undefined }, phase: "running" };
    render(<PlayHeader contestId="c1" title="X" waitingForStart={false} dict={en} />);

    expect(screen.getByRole("timer")).toHaveTextContent(en.participant.play.clock.syncing);
    expect(screen.getByRole("timer")).not.toHaveTextContent(en.participant.play.clock.notStarted);
  });

  // The page's own content reads start an individual participant's clock on
  // the server, possibly after the channel's first sync said there was no
  // deadline yet. Once the workspace has loaded, that sync is stale: the clock
  // asks for one fresh sync, once, and says it is synchronising meanwhile
  // rather than claiming the countdown has not started.
  test("once the workspace has loaded, a sync with no deadline reads as synchronising and asks for one resync", () => {
    const resync = vi.fn();
    events.current = { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "running", resync };
    const { rerender } = render(
      <ContentLoadedProvider>
        <PlayHeader contestId="c1" title="X" waitingForStart={false} dict={en} />
        <ContentLoadedSignal />
      </ContentLoadedProvider>,
    );

    expect(screen.getByRole("timer")).not.toHaveTextContent(en.participant.play.clock.notStarted);
    expect(screen.getByRole("timer")).toHaveTextContent(en.participant.play.clock.syncing);
    expect(resync).toHaveBeenCalledTimes(1);

    rerender(
      <ContentLoadedProvider>
        <PlayHeader contestId="c1" title="Y" waitingForStart={false} dict={en} />
        <ContentLoadedSignal />
      </ContentLoadedProvider>,
    );
    expect(resync).toHaveBeenCalledTimes(1);
  });

  test("asks for no resync before the workspace has loaded", () => {
    const resync = vi.fn();
    events.current = { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "running", resync };
    render(
      <ContentLoadedProvider>
        <PlayHeader contestId="c1" title="X" waitingForStart={false} dict={en} />
      </ContentLoadedProvider>,
    );

    expect(resync).not.toHaveBeenCalled();
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

  // Finding 1: the events channel this clock runs on can fail outright (a
  // connection limit, a rate limit, this account losing access) and the
  // hook exposes it as `channelError` — previously nothing on screen could
  // render it at all.
  test("shows a translated reason when the events channel itself fails", () => {
    events.current = {
      offsetRef: { current: 0 },
      deadlineRef: { current: null },
      phase: "running",
      channelError: "too_many_connections",
    };
    render(<PlayHeader contestId="c1" title="X" waitingForStart={false} dict={en} />);

    expect(screen.getByText(en.errors.too_many_connections)).toBeInTheDocument();
  });

  test("shows no channel notice when the channel has no error", () => {
    events.current = { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "running", channelError: null };
    render(<PlayHeader contestId="c1" title="X" waitingForStart={false} dict={en} />);

    expect(screen.queryByText(en.errors.too_many_connections)).not.toBeInTheDocument();
  });

  // Finding 6: nothing reads the clock's own snapshot while the contest is
  // waiting or finished, so ticking an interval for either is a render for
  // nothing.
  describe("the clock's own interval", () => {
    afterEach(() => {
      vi.restoreAllMocks();
    });

    test("does not tick while waiting", () => {
      const setIntervalSpy = vi.spyOn(window, "setInterval");
      events.current = { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "waiting" };
      render(<PlayHeader contestId="c1" title="X" waitingForStart dict={en} />);

      expect(setIntervalSpy).not.toHaveBeenCalled();
    });

    test("does not tick once finished", () => {
      const setIntervalSpy = vi.spyOn(window, "setInterval");
      events.current = { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "finished" };
      render(<PlayHeader contestId="c1" title="X" waitingForStart={false} dict={en} />);

      expect(setIntervalSpy).not.toHaveBeenCalled();
    });

    test("ticks while running", () => {
      const setIntervalSpy = vi.spyOn(window, "setInterval");
      events.current = { offsetRef: { current: 0 }, deadlineRef: { current: Date.now() + 90_000 }, phase: "running" };
      render(<PlayHeader contestId="c1" title="X" waitingForStart={false} dict={en} />);

      expect(setIntervalSpy).toHaveBeenCalled();
    });
  });

  // Finding 7: the clock is `aria-live="off"`, on purpose (reading a
  // two-hour countdown aloud every second would drown a screen reader user
  // in chatter) — but that means a threshold worth interrupting for has to
  // be announced some other way.
  describe("the clock's screen-reader announcement", () => {
    afterEach(() => {
      vi.useRealTimers();
    });

    test("announces five minutes remaining once the countdown crosses that threshold", () => {
      vi.useFakeTimers();
      const deadline = Date.now() + 5 * 60_000 + 1_000;
      events.current = { offsetRef: { current: 0 }, deadlineRef: { current: deadline }, phase: "running" };
      render(<PlayHeader contestId="c1" title="X" waitingForStart={false} dict={en} />);

      expect(screen.queryByText(en.participant.play.clock.fiveMinutesLeft)).not.toBeInTheDocument();

      act(() => {
        vi.advanceTimersByTime(1_000);
      });

      expect(screen.getByText(en.participant.play.clock.fiveMinutesLeft)).toBeInTheDocument();
    });

    test("announces time is up once the countdown reaches zero", () => {
      vi.useFakeTimers();
      const deadline = Date.now() + 1_000;
      events.current = { offsetRef: { current: 0 }, deadlineRef: { current: deadline }, phase: "running" };
      render(<PlayHeader contestId="c1" title="X" waitingForStart={false} dict={en} />);

      act(() => {
        vi.advanceTimersByTime(1_000);
      });

      // Two elements now carry this exact text: the visible, aria-live="off"
      // clock (unaffected — proven by the existing "does not disable
      // anything" test) and the hidden live region this test is actually
      // about. getAllByText, not getByText, is what that duplication calls
      // for.
      const matches = screen.getAllByText(en.participant.play.clock.timeUp);
      expect(matches.some((el) => el.getAttribute("aria-live") === "polite")).toBe(true);
    });
  });
});

/**
 * §8's three toggles live at the right end of this bar. They belong to the
 * workspace below it, which is a sibling behind a `<Suspense>` boundary, so
 * what the header actually renders is `PanelToggles` — a component that
 * draws nothing until there is a provider around the pair (panel-toggles.tsx
 * and its own tests).
 */
describe("the panel toggles", () => {
  const t = en.participant.play.workspace.panels;

  test("are in the bar when the workspace is below it", () => {
    events.current = { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "running" };
    render(
      <PanelVisibilityProvider contestId="c1">
        <PlayHeader contestId="c1" title="X" waitingForStart={false} dict={en} />
      </PanelVisibilityProvider>,
    );

    expect(screen.getByRole("button", { name: t.schema })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t.side })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t.bottom })).toBeInTheDocument();
  });

  // The waiting room draws this same bar with no workspace under it, and a
  // control for a panel that is not on the screen is a control for nothing.
  test("are absent in the waiting room", () => {
    events.current = { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "waiting" };
    render(<PlayHeader contestId="c1" title="X" waitingForStart dict={en} />);

    expect(screen.queryByRole("button", { name: t.bottom })).not.toBeInTheDocument();
  });
});
