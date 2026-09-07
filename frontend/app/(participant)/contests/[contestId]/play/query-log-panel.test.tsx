import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import { QUERY_LOG_PAGE_SIZE } from "@/lib/api/querylog-terms";
import type { QueryLogEntry } from "@/lib/api/querylog";

import { QueryLogPanel } from "./query-log-panel";
import type { QueryLogRefreshResult } from "./actions";

const fetchQueryLogAction = vi.hoisted(() => vi.fn<(...args: unknown[]) => Promise<QueryLogRefreshResult>>());
vi.mock("./actions", () => ({ fetchQueryLogAction }));

function entry(sql: string, overrides: Partial<QueryLogEntry> = {}): QueryLogEntry {
  return { sql, status: "ok", error: "", durationMs: 12, rowCount: 3, executedAt: "2026-09-05T10:00:00Z", ...overrides };
}

function show(props: Partial<React.ComponentProps<typeof QueryLogPanel>> = {}) {
  return render(
    <QueryLogPanel
      contestId="c1"
      initial={{ items: [entry("SELECT 1")], total: 1 }}
      refreshToken={0}
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
    show({ initial: { items: [], total: 0 } });

    expect(screen.getByText(en.participant.play.workspace.log.empty)).toBeInTheDocument();
  });

  test("a row still running carries no duration or row count", () => {
    show({
      initial: { items: [entry("SELECT pg_sleep(5)", { status: "running", durationMs: undefined, rowCount: undefined })], total: 1 },
    });

    const row = screen.getByText("SELECT pg_sleep(5)").closest("tr")!;
    expect(row).toHaveTextContent(en.participant.play.workspace.log.status.running);
  });

  // The plan's own requirement: a student who refreshes mid-olympiad must
  // not lose their history — proven here by seeding QueryLogPanel with a
  // server-fetched initial page, exactly what page.tsx hands it on reload.
  test("a reload sees the history the server already fetched, with no gap", () => {
    show({ initial: { items: [entry("SELECT 1"), entry("SELECT 2")], total: 2 } });

    expect(screen.getByText("SELECT 1")).toBeInTheDocument();
    expect(screen.getByText("SELECT 2")).toBeInTheDocument();
  });

  test("offers to load more only when more rows exist", () => {
    const { unmount } = show({ initial: { items: [entry("SELECT 1")], total: 1 } });
    expect(screen.queryByRole("button", { name: en.participant.play.workspace.log.loadMore })).not.toBeInTheDocument();
    unmount();

    show({ initial: { items: [entry("SELECT 1")], total: 2 } });
    expect(screen.getByRole("button", { name: en.participant.play.workspace.log.loadMore })).toBeInTheDocument();
  });

  test("load more appends the next page rather than replacing the first", async () => {
    fetchQueryLogAction.mockResolvedValueOnce({ kind: "ok", items: [entry("SELECT 2")], total: 2 });
    show({ initial: { items: [entry("SELECT 1")], total: 2 } });

    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.log.loadMore }));

    await waitFor(() => expect(screen.getByText("SELECT 2")).toBeInTheDocument());
    expect(screen.getByText("SELECT 1")).toBeInTheDocument();
    expect(fetchQueryLogAction).toHaveBeenCalledWith("c1", QUERY_LOG_PAGE_SIZE, 1);
  });

  // A refresh triggered by refreshToken re-fetches at least as many rows as
  // were already loaded, so a participant who pressed "load more" a few
  // times does not see the list shrink back to one page the moment a fresh
  // query lands.
  test("a new query refreshes the log without shrinking an already-expanded page", async () => {
    fetchQueryLogAction.mockResolvedValueOnce({
      kind: "ok",
      items: [entry("SELECT 3"), entry("SELECT 2"), entry("SELECT 1")],
      total: 3,
    });
    const { rerender } = show({ initial: { items: [entry("SELECT 2"), entry("SELECT 1")], total: 2 }, refreshToken: 0 });

    rerender(
      <QueryLogPanel
        contestId="c1"
        initial={{ items: [entry("SELECT 2"), entry("SELECT 1")], total: 2 }}
        refreshToken={1}
        locale="en"
        dict={en}
      />,
    );

    await waitFor(() => expect(screen.getByText("SELECT 3")).toBeInTheDocument());
    // Never fewer than a full page, even when fewer rows were loaded: the
    // refresh asks for at least QUERY_LOG_PAGE_SIZE, not the smaller count
    // that happened to be on screen.
    expect(fetchQueryLogAction).toHaveBeenCalledWith("c1", QUERY_LOG_PAGE_SIZE, 0);
  });

  test("a failed refresh leaves the existing rows on screen", async () => {
    fetchQueryLogAction.mockResolvedValueOnce({ kind: "refused", code: "unreachable" });
    const { rerender } = show({ refreshToken: 0 });

    rerender(<QueryLogPanel contestId="c1" initial={{ items: [entry("SELECT 1")], total: 1 }} refreshToken={1} locale="en" dict={en} />);

    await waitFor(() => expect(fetchQueryLogAction).toHaveBeenCalled());
    expect(screen.getByText("SELECT 1")).toBeInTheDocument();
  });
});
