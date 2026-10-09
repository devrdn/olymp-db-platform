import { act, render, screen, within } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import type { FeedDetail, FeedItem, FeedPage } from "@/lib/api/monitor";
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

import { MONITOR_POLL_MS } from "../use-monitor";
import { SESSION_KINDS, SessionsTab } from "./sessions-tab";
import { CONTEST, REG, setVisibility } from "./test-fixtures";

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

function item(cursor: string, kind: string, detail: FeedDetail, minute: number): FeedItem {
  return {
    cursor,
    at: `2026-09-20T10:${String(minute).padStart(2, "0")}:00.000Z`,
    kind,
    registrationId: REG,
    login: "ivanov",
    fullName: "Ivan Ivanov",
    detail,
  };
}

// Oldest first, as the timeline sends them.
const page: FeedPage = {
  items: [
    item("a", "sign_in", { type: "audit", ip: "10.0.0.1", userAgent: "Firefox 130" }, 1),
    item("b", "ip_changed", { type: "ip_changed", from: "10.0.0.1", to: "10.0.0.2" }, 2),
    item("c", "parallel_session", { type: "parallel_session", otherIp: "192.168.1.9", userAgent: "Chrome 128" }, 3),
    item("d", "sign_in_failed", { type: "audit", ip: "10.0.0.2" }, 4),
  ],
  more: false,
  newest: "d",
  oldest: "a",
};

const t = () => dict.workspace.monitor.participant.sessions;

function renderTab(initial: FeedPage = page) {
  return render(<SessionsTab contestId={CONTEST} registrationId={REG} feed={initial} dict={dict} locale="en" />);
}

describe("the sign-ins and networks tab", () => {
  test("lists each event newest first, with its address and browser", () => {
    renderTab();
    const rows = within(screen.getByRole("table")).getAllByRole("row").slice(1);
    expect(rows.map((row) => within(row).getAllByRole("cell")[1].textContent)).toEqual([
      t().events.sign_in_failed,
      t().events.parallel_session,
      t().events.ip_changed,
      t().events.sign_in,
    ]);
    expect(rows[1]).toHaveTextContent("192.168.1.9");
    expect(rows[1]).toHaveTextContent("Chrome 128");
    expect(rows[2]).toHaveTextContent("10.0.0.1 → 10.0.0.2");
    expect(rows[3]).toHaveTextContent("Firefox 130");
  });

  /** Up to a thousand rows; off-screen ones are skipped by the browser. */
  test("lets the browser skip rows out of view", () => {
    renderTab();
    for (const row of within(screen.getByRole("table")).getAllByRole("row").slice(1)) {
      expect(row.className).toContain("[content-visibility:auto]");
    }
  });

  test("names every address seen, once", () => {
    renderTab();
    const addresses = screen.getByRole("list", { name: t().addresses });
    expect(within(addresses).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      "10.0.0.1",
      "10.0.0.2",
      "192.168.1.9",
    ]);
  });

  test("keeps itself current from the timeline of those kinds while visible", async () => {
    fetchTimeline.mockResolvedValueOnce({
      items: [item("e", "sign_out", { type: "audit" }, 5)],
      more: false,
      newest: "e",
      oldest: "e",
    });
    renderTab();

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(fetchTimeline).toHaveBeenCalledWith(
      CONTEST,
      REG,
      expect.objectContaining({ after: "d", kinds: SESSION_KINDS }),
      expect.anything(),
    );
    expect(screen.getAllByRole("row")[1]).toHaveTextContent(t().events.sign_out);
    expect(fetchRoster).not.toHaveBeenCalled();

    await act(async () => setVisibility("hidden"));
    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS * 5));
    expect(fetchTimeline).toHaveBeenCalledTimes(1);
  });

  test("says when there is nothing yet", () => {
    renderTab({ items: [], more: false });
    expect(screen.getByText(t().empty)).toBeInTheDocument();
  });
});
