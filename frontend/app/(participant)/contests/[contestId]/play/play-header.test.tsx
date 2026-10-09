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
  reopened?: boolean;
};

// The hook is tested on its own; this stand-in drives the header through
// every phase. `real` hands over the hook itself for tests of the pair.
const events = vi.hoisted(() => ({
  current: { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "waiting" } as EventsSnapshot,
  real: false,
}));
vi.mock("./use-contest-events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./use-contest-events")>();
  return {
    useContestEvents: (...args: Parameters<typeof actual.useContestEvents>) =>
      events.real ? actual.useContestEvents(...args) : events.current,
  };
});

/** Just enough EventSource for the real hook to open a channel and receive a sync. */
class FakeEventSource {
  static instances: FakeEventSource[] = [];
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readyState: number = FakeEventSource.CONNECTING;
  private listeners = new Map<string, Set<(event: MessageEvent) => void>>();

  constructor() {
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, handler: (event: MessageEvent) => void) {
    if (!this.listeners.has(type)) this.listeners.set(type, new Set());
    this.listeners.get(type)!.add(handler);
  }

  emit(type: string, data: unknown) {
    const payload = { data: JSON.stringify(data) } as MessageEvent;
    for (const handler of this.listeners.get(type) ?? []) handler(payload);
  }

  close() {
    this.readyState = FakeEventSource.CLOSED;
  }
}

/** Admits the channel: the server's first sync on the open connection. */
function admitChannel() {
  act(() => FakeEventSource.instances.at(-1)!.emit("sync", { server_now: "2026-01-01T00:00:00.000Z" }));
}

import { ContentLoadedProvider, ContentLoadedSignal, RenderedRefusalSignal } from "./content-loaded";
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

  // jsdom cannot measure wrapping, so this checks both rules are declared
  // for the right ranges.
  test("the title truncates on a shared row and wraps on the narrow fallback", () => {
    events.current = { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "waiting" };
    render(<PlayHeader contestId="c1" title="The Greenhouse Case" waitingForStart dict={en} />);

    const title = screen.getByRole("heading", { level: 1 });
    expect(title.className).toMatch(/(^|\s)truncate(\s|$)/);
    expect(title.className).toMatch(/(^|\s)max-narrow:whitespace-normal(\s|$)/);
  });

  test("says under the title that the organiser sees this page", () => {
    events.current = { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "running" };
    render(<PlayHeader contestId="c1" title="The Greenhouse Case" waitingForStart={false} dict={en} />);

    expect(en.participant.play.observed).toBe(
      "The organiser sees your queries, answers, notes and actions on this page.",
    );
    expect(screen.getByText(en.participant.play.observed)).toBeInTheDocument();
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

  // Before the first sync the deadline is unknown; claiming the countdown
  // starts with the first action would be false in a fixed-window contest.
  test("says it is synchronising rather than claiming how the contest is timed", () => {
    events.current = { offsetRef: { current: 0 }, deadlineRef: { current: undefined }, phase: "running" };
    render(<PlayHeader contestId="c1" title="X" waitingForStart={false} dict={en} />);

    expect(screen.getByRole("timer")).toHaveTextContent(en.participant.play.clock.syncing);
    expect(screen.getByRole("timer")).not.toHaveTextContent(en.participant.play.clock.notStarted);
  });

  // The content reads can start an individual clock after a sync said there
  // was no deadline; once loaded, the clock asks for one resync and says it
  // is synchronising meanwhile.
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

    // The screen only reads the clock; nothing is disabled.
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

  // A page rendered "not open now" is stale once the channel reopens; only a
  // refresh brings the workspace.
  test("refreshes the page once when the channel reopens after the contest was not open", () => {
    events.current = { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "running", reopened: true };
    const { rerender } = render(<PlayHeader contestId="c1" title="X" waitingForStart={false} dict={en} />);

    expect(refresh).toHaveBeenCalledTimes(1);

    rerender(<PlayHeader contestId="c1" title="Y" waitingForStart={false} dict={en} />);
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  test("does not refresh on a channel that has not reopened", () => {
    events.current = { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "running", reopened: false };
    render(<PlayHeader contestId="c1" title="X" waitingForStart={false} dict={en} />);

    expect(refresh).not.toHaveBeenCalled();
  });

  // The window may open before the channel's first connection, which is then
  // simply admitted. The page says what it rendered, and the header refreshes
  // once both facts are known, in either order.
  describe("the page under the bar says what it rendered", () => {
    beforeEach(() => {
      events.real = true;
      FakeEventSource.instances = [];
      vi.stubGlobal("EventSource", FakeEventSource);
    });
    afterEach(() => {
      events.real = false;
      vi.unstubAllGlobals();
    });

    function under(signal: React.ReactNode, { title = "X", waitingForStart = false } = {}) {
      return (
        <ContentLoadedProvider>
          <PlayHeader contestId="c1" title={title} waitingForStart={waitingForStart} dict={en} />
          {signal}
        </ContentLoadedProvider>
      );
    }

    test("a page that rendered dormant refreshes once when its channel's first connection is admitted", () => {
      const { rerender } = render(under(<RenderedRefusalSignal code="contest_not_running" />));
      expect(refresh).not.toHaveBeenCalled();

      admitChannel();
      expect(refresh).toHaveBeenCalledTimes(1);

      admitChannel();
      rerender(under(<RenderedRefusalSignal code="contest_not_running" />, { title: "Y" }));
      expect(refresh).toHaveBeenCalledTimes(1);
    });

    // The page streams in, so the channel may be admitted first.
    test("a page that says it rendered dormant after its channel was admitted refreshes once", () => {
      const { rerender } = render(under(null));
      admitChannel();
      expect(refresh).not.toHaveBeenCalled();

      rerender(under(<RenderedRefusalSignal code="contest_not_running" />));

      expect(refresh).toHaveBeenCalledTimes(1);
    });

    test("a page that rendered normally does not refresh on connect", () => {
      render(under(<ContentLoadedSignal />));

      admitChannel();

      expect(refresh).not.toHaveBeenCalled();
    });

    test("a page that rendered a closed refusal never refreshes", () => {
      render(under(<RenderedRefusalSignal code="contest_ended" />));

      admitChannel();

      expect(refresh).not.toHaveBeenCalled();
    });

    test("the waiting room does not refresh on an admitted channel, whatever was signalled", () => {
      render(under(<RenderedRefusalSignal code="contest_not_running" />, { waitingForStart: true }));

      admitChannel();

      expect(refresh).not.toHaveBeenCalled();
    });
  });

  // The waiting room refreshes only on contest_started; a reopening alone
  // (a contest republished after draft) leaves it waiting.
  test("leaves the waiting room alone when its channel reopens", () => {
    events.current = { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "waiting", reopened: true };
    render(<PlayHeader contestId="c1" title="X" waitingForStart dict={en} />);

    expect(refresh).not.toHaveBeenCalled();
  });

  test("marks the contest finished once the channel says so", () => {
    events.current = { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "finished" };
    render(<PlayHeader contestId="c1" title="X" waitingForStart={false} dict={en} />);

    expect(screen.getByText(en.participant.play.finishedTag)).toBeInTheDocument();
  });

  // A failed channel (connection or rate limit, lost access) is shown.
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

  // Nothing reads the snapshot while waiting or finished, so no interval.
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

  // The clock is `aria-live="off"` so a screen reader is not read every
  // second; thresholds are announced separately.
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

      // Two elements carry this text: the visible clock and the hidden live
      // region.
      const matches = screen.getAllByText(en.participant.play.clock.timeUp);
      expect(matches.some((el) => el.getAttribute("aria-live") === "polite")).toBe(true);
    });
  });
});

/**
 * The panel toggles at the bar's right end. They control the workspace behind the
 * Suspense boundary, so `PanelToggles` draws nothing without a provider.
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

  // The waiting room has no panels to control.
  test("are absent in the waiting room", () => {
    events.current = { offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "waiting" };
    render(<PlayHeader contestId="c1" title="X" waitingForStart dict={en} />);

    expect(screen.queryByRole("button", { name: t.bottom })).not.toBeInTheDocument();
  });
});
