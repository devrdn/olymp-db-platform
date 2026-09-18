import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import type { FeedPage } from "@/lib/api/monitor";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

const { fetchTimeline, fetchFeed, fetchRoster } = vi.hoisted(() => ({
  fetchTimeline: vi.fn(),
  fetchFeed: vi.fn(),
  fetchRoster: vi.fn(),
}));
vi.mock("@/lib/api/monitor", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/monitor")>()),
  fetchTimeline,
  fetchFeed,
  fetchRoster,
}));
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh: vi.fn() }) }));

import { feedItem } from "../test-fixtures";
import { MONITOR_POLL_MS } from "../use-monitor";
import { CONTEST, REG, setVisibility } from "./test-fixtures";
import { TimelineTab } from "./timeline-tab";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

beforeEach(() => {
  vi.useFakeTimers();
  vi.resetAllMocks();
  setVisibility("visible");
  fetchTimeline.mockResolvedValue({ items: [], more: false });
});

afterEach(() => {
  vi.useRealTimers();
});

const page: FeedPage = {
  items: [feedItem("c1", { registrationId: REG, fullName: "Ivan Ivanov" })],
  more: false,
  newest: "c1",
  oldest: "c1",
};

const t = () => dict.workspace.monitor.participant.range;

function renderTab() {
  return render(<TimelineTab contestId={CONTEST} registrationId={REG} feed={page} dict={dict} locale="en" />);
}

describe("the timeline tab", () => {
  test("shows the participant's feed without naming them on every line", () => {
    renderTab();
    expect(screen.getByText("SELECT c1")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Ivan Ivanov" })).not.toBeInTheDocument();
  });

  test("polls the participant's timeline while visible, and stops while hidden", async () => {
    renderTab();
    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(fetchTimeline).toHaveBeenCalledTimes(1);
    expect(fetchTimeline).toHaveBeenCalledWith(CONTEST, REG, expect.objectContaining({ after: "c1" }), expect.anything());
    expect(fetchFeed).not.toHaveBeenCalled();

    await act(async () => setVisibility("hidden"));
    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS * 4));
    expect(fetchTimeline).toHaveBeenCalledTimes(1);
  });

  test("a time range, read in the contest's time zone, narrows the feed", async () => {
    renderTab();
    fireEvent.change(screen.getByLabelText(t().from), { target: { value: "2026-09-20T12:00" } });
    fireEvent.change(screen.getByLabelText(t().until), { target: { value: "2026-09-20T13:30" } });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: t().apply }));
    });

    expect(fetchTimeline).toHaveBeenLastCalledWith(
      CONTEST,
      REG,
      { kinds: [], from: "2026-09-20T09:00:00.000Z", until: "2026-09-20T10:30:00.000Z", limit: 200 },
      expect.anything(),
    );

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: t().clear }));
    });
    expect(fetchTimeline).toHaveBeenLastCalledWith(CONTEST, REG, { kinds: [], limit: 200 }, expect.anything());
    expect(screen.getByLabelText(t().from)).toHaveValue("");
  });

  test("a range that ends before it begins is refused", async () => {
    renderTab();
    fireEvent.change(screen.getByLabelText(t().from), { target: { value: "2026-09-20T12:00" } });
    fireEvent.change(screen.getByLabelText(t().until), { target: { value: "2026-09-20T11:00" } });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: t().apply }));
    });

    expect(screen.getByText(t().invalid)).toBeInTheDocument();
    expect(fetchTimeline).not.toHaveBeenCalled();
  });
});
