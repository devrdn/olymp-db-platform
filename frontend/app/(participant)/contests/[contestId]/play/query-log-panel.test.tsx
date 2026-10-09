import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import { QUERY_LOG_PAGE_SIZE, QUERY_LOG_REFRESH_MIN_INTERVAL_MS } from "@/lib/api/querylog-terms";
import type { QueryLogEntry } from "@/lib/api/querylog";

import { QueryLogPanel } from "./query-log-panel";
import type { QueryLogRefreshResult } from "./actions";

const fetchQueryLogAction = vi.hoisted(() => vi.fn<(...args: unknown[]) => Promise<QueryLogRefreshResult>>());
vi.mock("./actions", () => ({ fetchQueryLogAction }));

function entry(sql: string, overrides: Partial<QueryLogEntry> = {}): QueryLogEntry {
  return {
    sql,
    sqlTruncated: false,
    status: "ok",
    error: "",
    durationMs: 12,
    rowCount: 3,
    executedAt: "2026-09-05T10:00:00Z",
    ...overrides,
  };
}

function show(props: Partial<React.ComponentProps<typeof QueryLogPanel>> = {}) {
  return render(
    <QueryLogPanel
      contestId="c1"
      initial={{ items: [entry("SELECT 1")], total: 1, failed: false }}
      active
      locale="en"
      dict={en}
      {...props}
    />,
  );
}

describe("the query log panel", () => {
  beforeEach(() => {
    fetchQueryLogAction.mockReset();
  });

  test("renders the initial page without a client fetch", () => {
    show();

    expect(screen.getByText("SELECT 1")).toBeInTheDocument();
    expect(fetchQueryLogAction).not.toHaveBeenCalled();
  });

  test("says nothing has run yet when the log is empty", () => {
    show({ initial: { items: [], total: 0, failed: false } });

    expect(screen.getByText(en.participant.play.workspace.log.empty)).toBeInTheDocument();
  });

  // An unreadable log must not look empty: the participant could not tell,
  // and a zero `total` hides "load more" too.
  test("a log the server could not read says so, not that nothing has run yet", () => {
    show({ initial: { items: [], total: 0, failed: true } });

    expect(screen.queryByText(en.participant.play.workspace.log.empty)).not.toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent(en.participant.play.workspace.log.failed);
    expect(screen.getByRole("button", { name: en.participant.play.workspace.log.retry })).toBeInTheDocument();
  });

  test("retrying a failed empty log fetches the first page again", async () => {
    fetchQueryLogAction.mockResolvedValueOnce({ kind: "ok", items: [entry("SELECT 1")], total: 1 });
    show({ initial: { items: [], total: 0, failed: true } });

    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.log.retry }));

    await waitFor(() => expect(screen.getByText("SELECT 1")).toBeInTheDocument());
    expect(fetchQueryLogAction).toHaveBeenCalledWith("c1", QUERY_LOG_PAGE_SIZE, 0);
  });

  test("a row still running carries no duration or row count", () => {
    show({
      initial: { items: [entry("SELECT pg_sleep(5)", { status: "running", durationMs: undefined, rowCount: undefined })], total: 1, failed: false },
    });

    const row = screen.getByText("SELECT pg_sleep(5)").closest("tr")!;
    expect(row).toHaveTextContent(en.participant.play.workspace.log.status.running);
  });

  // A long statement arrives truncated; the ellipsis, also in the tooltip,
  // says so. The CSV has the whole text.
  test("says when a statement arrived cut short", () => {
    show({ initial: { items: [entry("SELECT 'xxxx", { sqlTruncated: true })], total: 1, failed: false } });

    expect(screen.getByText("SELECT 'xxxx…")).toBeInTheDocument();
    expect(screen.getByTitle("SELECT 'xxxx…")).toBeInTheDocument();
  });

  // A reload keeps the history: the panel is seeded with the server-fetched
  // page, as page.tsx does.
  test("a reload sees the history the server already fetched, with no gap", () => {
    show({ initial: { items: [entry("SELECT 1"), entry("SELECT 2")], total: 2, failed: false } });

    expect(screen.getByText("SELECT 1")).toBeInTheDocument();
    expect(screen.getByText("SELECT 2")).toBeInTheDocument();
  });

  test("offers to load more only when more rows exist", () => {
    const { unmount } = show({ initial: { items: [entry("SELECT 1")], total: 1, failed: false } });
    expect(screen.queryByRole("button", { name: en.participant.play.workspace.log.loadMore })).not.toBeInTheDocument();
    unmount();

    show({ initial: { items: [entry("SELECT 1")], total: 2, failed: false } });
    expect(screen.getByRole("button", { name: en.participant.play.workspace.log.loadMore })).toBeInTheDocument();
  });

  test("load more appends the next page rather than replacing the first", async () => {
    fetchQueryLogAction.mockResolvedValueOnce({ kind: "ok", items: [entry("SELECT 2")], total: 2 });
    show({ initial: { items: [entry("SELECT 1")], total: 2, failed: false } });

    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.log.loadMore }));

    await waitFor(() => expect(screen.getByText("SELECT 2")).toBeInTheDocument());
    expect(screen.getByText("SELECT 1")).toBeInTheDocument();
    expect(fetchQueryLogAction).toHaveBeenCalledWith("c1", QUERY_LOG_PAGE_SIZE, 1);
  });

  test("a failed load more leaves the existing rows on screen, with a way to retry", async () => {
    fetchQueryLogAction.mockResolvedValueOnce({ kind: "refused", code: "unreachable" });
    show({ initial: { items: [entry("SELECT 1")], total: 2, failed: false } });

    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.log.loadMore }));

    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent(en.participant.play.workspace.log.failed));
    expect(screen.getByText("SELECT 1")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: en.participant.play.workspace.log.retry })).toBeInTheDocument();
  });

  // It refreshes on becoming shown, never on mount for data page.tsx already
  // fetched.
  test("does not refetch on mount even when it starts active", () => {
    show({ active: true });

    expect(fetchQueryLogAction).not.toHaveBeenCalled();
  });

  test("refetches on the transition into being the active tab", async () => {
    fetchQueryLogAction.mockResolvedValueOnce({ kind: "ok", items: [entry("SELECT 2"), entry("SELECT 1")], total: 2 });
    const { rerender } = show({ active: false, initial: { items: [entry("SELECT 1")], total: 1, failed: false } });
    expect(fetchQueryLogAction).not.toHaveBeenCalled();

    rerender(
      <QueryLogPanel
        contestId="c1"
        initial={{ items: [entry("SELECT 1")], total: 1, failed: false }}
        active
        locale="en"
        dict={en}
      />,
    );

    await waitFor(() => expect(screen.getByText("SELECT 2")).toBeInTheDocument());
    // At least a full page, even when fewer rows were loaded.
    expect(fetchQueryLogAction).toHaveBeenCalledWith("c1", QUERY_LOG_PAGE_SIZE, 0);
  });

  test("does not refetch again while it stays the active tab", async () => {
    const { rerender } = show({ active: true });
    expect(fetchQueryLogAction).not.toHaveBeenCalled();

    rerender(<QueryLogPanel contestId="c1" initial={{ items: [entry("SELECT 1")], total: 1, failed: false }} active locale="en" dict={en} />);

    expect(fetchQueryLogAction).not.toHaveBeenCalled();
  });

  test("a failed refresh on becoming active leaves the existing rows on screen", async () => {
    fetchQueryLogAction.mockResolvedValueOnce({ kind: "refused", code: "unreachable" });
    const { rerender } = show({ active: false });

    rerender(<QueryLogPanel contestId="c1" initial={{ items: [entry("SELECT 1")], total: 1, failed: false }} active locale="en" dict={en} />);

    await waitFor(() => expect(fetchQueryLogAction).toHaveBeenCalled());
    expect(screen.getByText("SELECT 1")).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent(en.participant.play.workspace.log.failed);
  });

  // Toggling back within QUERY_LOG_REFRESH_MIN_INTERVAL_MS must not spend
  // another AdmitRead, the budget Run shares.
  test("does not refetch a second time when toggled back within the minimum refresh interval", async () => {
    fetchQueryLogAction.mockResolvedValueOnce({ kind: "ok", items: [entry("SELECT 1")], total: 1 });
    const { rerender } = show({ active: false, initial: { items: [entry("SELECT 1")], total: 1, failed: false } });

    rerender(<QueryLogPanel contestId="c1" initial={{ items: [entry("SELECT 1")], total: 1, failed: false }} active locale="en" dict={en} />);
    await waitFor(() => expect(fetchQueryLogAction).toHaveBeenCalledTimes(1));

    // Leave and come straight back, inside the interval.
    rerender(<QueryLogPanel contestId="c1" initial={{ items: [entry("SELECT 1")], total: 1, failed: false }} active={false} locale="en" dict={en} />);
    rerender(<QueryLogPanel contestId="c1" initial={{ items: [entry("SELECT 1")], total: 1, failed: false }} active locale="en" dict={en} />);

    expect(fetchQueryLogAction).toHaveBeenCalledTimes(1);
  });

  // Overlapping refreshes: the newest request wins, not the last to resolve.
  test("a slower, superseded refresh cannot overwrite what a newer one already set", async () => {
    vi.useFakeTimers();
    try {
      let resolveFirst!: (value: QueryLogRefreshResult) => void;
      let resolveSecond!: (value: QueryLogRefreshResult) => void;
      fetchQueryLogAction
        .mockImplementationOnce(() => new Promise((resolve) => { resolveFirst = resolve; }))
        .mockImplementationOnce(() => new Promise((resolve) => { resolveSecond = resolve; }));

      const { rerender } = show({ active: false, initial: { items: [entry("SELECT 1")], total: 1, failed: false } });
      rerender(<QueryLogPanel contestId="c1" initial={{ items: [entry("SELECT 1")], total: 1, failed: false }} active locale="en" dict={en} />);
      await vi.waitFor(() => expect(fetchQueryLogAction).toHaveBeenCalledTimes(1));

      // Past the interval, so a second transition refreshes while the first is
      // pending.
      await vi.advanceTimersByTimeAsync(QUERY_LOG_REFRESH_MIN_INTERVAL_MS + 1);
      rerender(<QueryLogPanel contestId="c1" initial={{ items: [entry("SELECT 1")], total: 1, failed: false }} active={false} locale="en" dict={en} />);
      rerender(<QueryLogPanel contestId="c1" initial={{ items: [entry("SELECT 1")], total: 1, failed: false }} active locale="en" dict={en} />);
      await vi.waitFor(() => expect(fetchQueryLogAction).toHaveBeenCalledTimes(2));

      // The newer request settles first...
      resolveSecond({ kind: "ok", items: [entry("SELECT 2")], total: 1 });
      await vi.waitFor(() => expect(screen.getByText("SELECT 2")).toBeInTheDocument());

      // ...and the stale one after. It must not win.
      resolveFirst({ kind: "ok", items: [entry("SELECT 1")], total: 1 });
      await vi.runAllTimersAsync();

      expect(screen.getByText("SELECT 2")).toBeInTheDocument();
      expect(screen.queryByText("SELECT 1")).not.toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });
});
