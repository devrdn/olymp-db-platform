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
    id: "e1",
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

  // Finding 4: an empty log and a log the server could not read must not
  // look identical — a participant checking what they already tried has no
  // way to tell a real answer from a shrug otherwise, and `total` being zero
  // hides the ordinary "load more" retry too.
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

  // One page of the log is bounded in bytes as well as in rows, so a very
  // long statement arrives as its beginning. Drawing that prefix as if it
  // were the whole query hands a student a shortened copy of their own text
  // — including in the tooltip, which is where the full statement otherwise
  // is. The ellipsis is what says there was more; the whole of it is in the
  // CSV export this panel already offers.
  test("says when a statement arrived cut short", () => {
    show({ initial: { items: [entry("SELECT 'xxxx", { sqlTruncated: true })], total: 1, failed: false } });

    expect(screen.getByText("SELECT 'xxxx…")).toBeInTheDocument();
    expect(screen.getByTitle("SELECT 'xxxx…")).toBeInTheDocument();
  });

  // The plan's own requirement: a student who refreshes mid-olympiad must
  // not lose their history — proven here by seeding QueryLogPanel with a
  // server-fetched initial page, exactly what page.tsx hands it on reload.
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

  // Finding 3: this panel stays mounted at all times, so it has to notice a
  // query that ran while it was hidden — but only by refetching on the
  // transition into being shown, never on mount for data page.tsx already
  // fetched server-side.
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
    // Never fewer than a full page, even when fewer rows were loaded: the
    // refresh asks for at least QUERY_LOG_PAGE_SIZE, not the smaller count
    // that happened to be on screen.
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

  // Finding 4 of the follow-up review: a student idly toggling Result and
  // Log spends one AdmitRead — Run's own shared budget — on every single
  // transition into "Log", even back into data just fetched a moment ago.
  // A second transition inside QUERY_LOG_REFRESH_MIN_INTERVAL_MS of the
  // first must not spend a second one.
  test("does not refetch a second time when toggled back within the minimum refresh interval", async () => {
    fetchQueryLogAction.mockResolvedValueOnce({ kind: "ok", items: [entry("SELECT 1")], total: 1 });
    const { rerender } = show({ active: false, initial: { items: [entry("SELECT 1")], total: 1, failed: false } });

    rerender(<QueryLogPanel contestId="c1" initial={{ items: [entry("SELECT 1")], total: 1, failed: false }} active locale="en" dict={en} />);
    await waitFor(() => expect(fetchQueryLogAction).toHaveBeenCalledTimes(1));

    // Leave, then come straight back — well inside the minimum interval.
    rerender(<QueryLogPanel contestId="c1" initial={{ items: [entry("SELECT 1")], total: 1, failed: false }} active={false} locale="en" dict={en} />);
    rerender(<QueryLogPanel contestId="c1" initial={{ items: [entry("SELECT 1")], total: 1, failed: false }} active locale="en" dict={en} />);

    expect(fetchQueryLogAction).toHaveBeenCalledTimes(1);
  });

  // Finding 4 of the follow-up review, the other half: two refreshes can
  // still overlap once enough real time separates the transitions that
  // triggered them (a slow first request still in flight when a later
  // transition starts a second). Whichever answer is actually newest must
  // win, never whichever happens to resolve last on the wire.
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

      // Far enough past the minimum interval for a second transition to
      // trigger its own refresh, while the first request is still pending.
      await vi.advanceTimersByTimeAsync(QUERY_LOG_REFRESH_MIN_INTERVAL_MS + 1);
      rerender(<QueryLogPanel contestId="c1" initial={{ items: [entry("SELECT 1")], total: 1, failed: false }} active={false} locale="en" dict={en} />);
      rerender(<QueryLogPanel contestId="c1" initial={{ items: [entry("SELECT 1")], total: 1, failed: false }} active locale="en" dict={en} />);
      await vi.waitFor(() => expect(fetchQueryLogAction).toHaveBeenCalledTimes(2));

      // The newer request settles first, with the truly current answer...
      resolveSecond({ kind: "ok", items: [entry("SELECT 2")], total: 1 });
      await vi.waitFor(() => expect(screen.getByText("SELECT 2")).toBeInTheDocument());

      // ...and the older, now-stale one settles after. It must not win.
      resolveFirst({ kind: "ok", items: [entry("SELECT 1")], total: 1 });
      await vi.runAllTimersAsync();

      expect(screen.getByText("SELECT 2")).toBeInTheDocument();
      expect(screen.queryByText("SELECT 1")).not.toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });
});
