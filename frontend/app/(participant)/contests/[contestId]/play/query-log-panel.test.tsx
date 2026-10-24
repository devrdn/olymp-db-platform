import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import { QUERY_LOG_PAGE_SIZE, QUERY_LOG_REFRESH_MIN_INTERVAL_MS } from "@/lib/api/querylog-terms";
import type { QueryLogEntry } from "@/lib/api/querylog";

import { QueryLogPanel } from "./query-log-panel";
import type { QueryLogEntryResult, QueryLogRefreshResult } from "./requests";

const fetchQueryLog = vi.hoisted(() => vi.fn<(...args: unknown[]) => Promise<QueryLogRefreshResult>>());
const fetchQueryLogEntry = vi.hoisted(() => vi.fn<(contestId: string, id: number) => Promise<QueryLogEntryResult>>());
vi.mock("./requests", () => ({ fetchQueryLog, fetchQueryLogEntry }));

let nextId = 1;

function entry(sql: string, overrides: Partial<QueryLogEntry> = {}): QueryLogEntry {
  return {
    id: nextId++,
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
    fetchQueryLog.mockReset();
  });

  test("renders the initial page without a client fetch", () => {
    show();

    expect(screen.getByText("SELECT 1")).toBeInTheDocument();
    expect(fetchQueryLog).not.toHaveBeenCalled();
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
    fetchQueryLog.mockResolvedValueOnce({ kind: "ok", items: [entry("SELECT 1")], total: 1 });
    show({ initial: { items: [], total: 0, failed: true } });

    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.log.retry }));

    await waitFor(() => expect(screen.getByText("SELECT 1")).toBeInTheDocument());
    expect(fetchQueryLog).toHaveBeenCalledWith("c1", QUERY_LOG_PAGE_SIZE, 0);
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

  test("load more appends the rows older than the last one held", async () => {
    fetchQueryLog.mockResolvedValueOnce({ kind: "ok", items: [entry("SELECT 2")], total: 2 });
    const first = entry("SELECT 1");
    show({ initial: { items: [first], total: 2, failed: false } });

    await userEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.log.loadMore }));

    await waitFor(() => expect(screen.getByText("SELECT 2")).toBeInTheDocument());
    expect(screen.getByText("SELECT 1")).toBeInTheDocument();
    expect(fetchQueryLog).toHaveBeenCalledWith("c1", QUERY_LOG_PAGE_SIZE, 0, first.id);
  });

  // However the page came to repeat a row (a refresh racing it, an older
  // server paging by offset), one row is one row: ids are the rows' keys.
  test("a row the next page repeats is shown once, and stays as it was", async () => {
    const user = userEvent.setup();
    const kept = entry("SELECT kept");
    fetchQueryLog.mockResolvedValueOnce({ kind: "ok", items: [{ ...kept }, entry("SELECT older")], total: 3 });
    show({ initial: { items: [entry("SELECT newest"), kept], total: 3, failed: false } });
    const row = () => screen.getAllByRole("row").filter((r) => r.textContent?.includes("SELECT kept"));
    await user.click(within(row()[0]).getByRole("button", { name: en.participant.play.workspace.log.show }));

    await user.click(screen.getByRole("button", { name: en.participant.play.workspace.log.loadMore }));

    await waitFor(() => expect(screen.getByText("SELECT older")).toBeInTheDocument());
    expect(screen.getAllByText("SELECT kept", { selector: "span" })).toHaveLength(1);
    expect(within(row()[0]).getByRole("button", { name: en.participant.play.workspace.log.hide })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
  });

  // The refresh replaces the list, so the page's cursor may no longer be the
  // last row held; appending the page would leave a silent gap above it.
  test("a page of older rows is dropped when a refresh started while it loaded", async () => {
    const user = userEvent.setup();
    let resolvePage!: (value: QueryLogRefreshResult) => void;
    let resolveRefresh!: (value: QueryLogRefreshResult) => void;
    fetchQueryLog
      .mockImplementationOnce(() => new Promise((resolve) => { resolvePage = resolve; }))
      .mockImplementationOnce(() => new Promise((resolve) => { resolveRefresh = resolve; }));
    const r1 = entry("SELECT r1");
    const r2 = entry("SELECT r2");
    const initial = { items: [r1, r2], total: 3, failed: false };
    const { rerender } = show({ active: false, initial });

    await user.click(screen.getByRole("button", { name: en.participant.play.workspace.log.loadMore }));
    rerender(<QueryLogPanel contestId="c1" initial={initial} active locale="en" dict={en} />);
    await waitFor(() => expect(fetchQueryLog).toHaveBeenCalledTimes(2));

    resolveRefresh({ kind: "ok", items: [entry("SELECT n1"), r1], total: 4 });
    await waitFor(() => expect(screen.getByText("SELECT n1")).toBeInTheDocument());
    resolvePage({ kind: "ok", items: [entry("SELECT r3")], total: 3 });

    await waitFor(() =>
      expect(screen.queryByRole("button", { name: en.participant.play.workspace.log.loadingMore })).not.toBeInTheDocument(),
    );
    expect(screen.queryByText("SELECT r3")).not.toBeInTheDocument();
    expect(screen.queryByText("SELECT r2")).not.toBeInTheDocument();
  });

  test("a failed load more leaves the existing rows on screen, with a way to retry", async () => {
    fetchQueryLog.mockResolvedValueOnce({ kind: "refused", code: "unreachable" });
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

    expect(fetchQueryLog).not.toHaveBeenCalled();
  });

  test("refetches on the transition into being the active tab", async () => {
    fetchQueryLog.mockResolvedValueOnce({ kind: "ok", items: [entry("SELECT 2"), entry("SELECT 1")], total: 2 });
    const { rerender } = show({ active: false, initial: { items: [entry("SELECT 1")], total: 1, failed: false } });
    expect(fetchQueryLog).not.toHaveBeenCalled();

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
    expect(fetchQueryLog).toHaveBeenCalledWith("c1", QUERY_LOG_PAGE_SIZE, 0);
  });

  test("does not refetch again while it stays the active tab", async () => {
    const { rerender } = show({ active: true });
    expect(fetchQueryLog).not.toHaveBeenCalled();

    rerender(<QueryLogPanel contestId="c1" initial={{ items: [entry("SELECT 1")], total: 1, failed: false }} active locale="en" dict={en} />);

    expect(fetchQueryLog).not.toHaveBeenCalled();
  });

  test("a failed refresh on becoming active leaves the existing rows on screen", async () => {
    fetchQueryLog.mockResolvedValueOnce({ kind: "refused", code: "unreachable" });
    const { rerender } = show({ active: false });

    rerender(<QueryLogPanel contestId="c1" initial={{ items: [entry("SELECT 1")], total: 1, failed: false }} active locale="en" dict={en} />);

    await waitFor(() => expect(fetchQueryLog).toHaveBeenCalled());
    expect(screen.getByText("SELECT 1")).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent(en.participant.play.workspace.log.failed);
  });

  // Toggling back within QUERY_LOG_REFRESH_MIN_INTERVAL_MS must not spend
  // another read of the play screen's budget.
  test("does not refetch a second time when toggled back within the minimum refresh interval", async () => {
    fetchQueryLog.mockResolvedValueOnce({ kind: "ok", items: [entry("SELECT 1")], total: 1 });
    const { rerender } = show({ active: false, initial: { items: [entry("SELECT 1")], total: 1, failed: false } });

    rerender(<QueryLogPanel contestId="c1" initial={{ items: [entry("SELECT 1")], total: 1, failed: false }} active locale="en" dict={en} />);
    await waitFor(() => expect(fetchQueryLog).toHaveBeenCalledTimes(1));

    // Leave and come straight back, inside the interval.
    rerender(<QueryLogPanel contestId="c1" initial={{ items: [entry("SELECT 1")], total: 1, failed: false }} active={false} locale="en" dict={en} />);
    rerender(<QueryLogPanel contestId="c1" initial={{ items: [entry("SELECT 1")], total: 1, failed: false }} active locale="en" dict={en} />);

    expect(fetchQueryLog).toHaveBeenCalledTimes(1);
  });

  // Overlapping refreshes: the newest request wins, not the last to resolve.
  test("a slower, superseded refresh cannot overwrite what a newer one already set", async () => {
    vi.useFakeTimers();
    try {
      let resolveFirst!: (value: QueryLogRefreshResult) => void;
      let resolveSecond!: (value: QueryLogRefreshResult) => void;
      fetchQueryLog
        .mockImplementationOnce(() => new Promise((resolve) => { resolveFirst = resolve; }))
        .mockImplementationOnce(() => new Promise((resolve) => { resolveSecond = resolve; }));

      const { rerender } = show({ active: false, initial: { items: [entry("SELECT 1")], total: 1, failed: false } });
      rerender(<QueryLogPanel contestId="c1" initial={{ items: [entry("SELECT 1")], total: 1, failed: false }} active locale="en" dict={en} />);
      await vi.waitFor(() => expect(fetchQueryLog).toHaveBeenCalledTimes(1));

      // Past the interval, so a second transition refreshes while the first is
      // pending.
      await vi.advanceTimersByTimeAsync(QUERY_LOG_REFRESH_MIN_INTERVAL_MS + 1);
      rerender(<QueryLogPanel contestId="c1" initial={{ items: [entry("SELECT 1")], total: 1, failed: false }} active={false} locale="en" dict={en} />);
      rerender(<QueryLogPanel contestId="c1" initial={{ items: [entry("SELECT 1")], total: 1, failed: false }} active locale="en" dict={en} />);
      await vi.waitFor(() => expect(fetchQueryLog).toHaveBeenCalledTimes(2));

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

/**
 * A row opens to its whole statement with a Copy button. A page cuts long
 * statements, so a cut one is read whole from the server on opening.
 */
describe("opening one query whole", () => {
  const t = en.participant.play.workspace.log;
  /** Longer than a page carries, so the page holds only its start. */
  const WHOLE = `SELECT '${"x".repeat(4000)}'`;
  const CUT = WHOLE.slice(0, 1000);

  beforeEach(() => {
    fetchQueryLog.mockReset();
    fetchQueryLogEntry.mockReset();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  /** The row a statement starts with, and its Show button. */
  function rowOf(text: string) {
    return screen.getAllByRole("row").find((row) => row.textContent?.includes(text)) as HTMLElement;
  }

  function toggle(text: string) {
    return within(rowOf(text)).getByRole("button", { name: /^(Show|Hide)$/ });
  }

  function statement() {
    return screen.getByRole("region", { name: t.statement });
  }

  test("a statement that arrived whole opens at once, asking the server nothing", async () => {
    const user = userEvent.setup();
    show({ initial: { items: [entry("SELECT name\nFROM guests")], total: 1, failed: false } });

    const button = toggle("SELECT name");
    expect(button).toHaveAttribute("aria-expanded", "false");
    await user.click(button);

    expect(button).toHaveAttribute("aria-expanded", "true");
    expect(button).toHaveTextContent(t.hide);
    expect(statement().querySelector("pre")?.textContent).toBe("SELECT name\nFROM guests");
    expect(fetchQueryLogEntry).not.toHaveBeenCalled();
  });

  test("a statement cut short is read whole when opened, once", async () => {
    const user = userEvent.setup();
    let release: (result: QueryLogEntryResult) => void = () => {};
    fetchQueryLogEntry.mockImplementationOnce(() => new Promise((resolve) => (release = resolve)));
    show({ initial: { items: [entry(CUT, { id: 7, sqlTruncated: true })], total: 1, failed: false } });

    await user.click(toggle(CUT));
    expect(within(statement()).getByText(t.loadingStatement)).toBeInTheDocument();
    release({ kind: "ok", entry: entry(WHOLE, { id: 7 }) });

    await waitFor(() => expect(statement().querySelector("pre")).toHaveTextContent(WHOLE));
    expect(fetchQueryLogEntry).toHaveBeenCalledWith("c1", 7);

    // Closed and opened again, it is not read a second time.
    await user.click(toggle(CUT));
    await user.click(toggle(CUT));
    expect(statement().querySelector("pre")).toHaveTextContent(WHOLE);
    expect(fetchQueryLogEntry).toHaveBeenCalledTimes(1);
  });

  test("Copy puts the whole statement on the clipboard, not the page's start of it", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn<(text: string) => Promise<void>>(async () => {});
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    fetchQueryLogEntry.mockResolvedValueOnce({ kind: "ok", entry: entry(WHOLE, { id: 7 }) });
    show({ initial: { items: [entry(CUT, { id: 7, sqlTruncated: true })], total: 1, failed: false } });

    await user.click(toggle(CUT));
    await user.click(await within(statement()).findByRole("button", { name: t.copy }));

    expect(writeText).toHaveBeenCalledWith(WHOLE);
    expect(within(statement()).getByText(t.copied)).toBeInTheDocument();
  });

  test("a failed read says why, and a retry reads it again", async () => {
    const user = userEvent.setup();
    fetchQueryLogEntry
      .mockResolvedValueOnce({ kind: "refused", code: "play_read_too_often" })
      .mockResolvedValueOnce({ kind: "ok", entry: entry(WHOLE, { id: 7 }) });
    show({ initial: { items: [entry(CUT, { id: 7, sqlTruncated: true })], total: 1, failed: false } });

    await user.click(toggle(CUT));
    expect(await within(statement()).findByText(en.errors.play_read_too_often)).toBeInTheDocument();
    expect(within(statement()).queryByRole("button", { name: t.copy })).not.toBeInTheDocument();

    await user.click(within(statement()).getByRole("button", { name: t.retry }));

    await waitFor(() => expect(statement().querySelector("pre")).toHaveTextContent(WHOLE));
  });

  test("says so when even the row read on its own is not the whole statement", async () => {
    const user = userEvent.setup();
    fetchQueryLogEntry.mockResolvedValueOnce({ kind: "ok", entry: entry(WHOLE, { id: 7, sqlTruncated: true }) });
    show({ initial: { items: [entry(CUT, { id: 7, sqlTruncated: true })], total: 1, failed: false } });

    await user.click(toggle(CUT));

    expect(await within(statement()).findByText(t.shortened)).toBeInTheDocument();
  });

  test("opens and closes from the keyboard", async () => {
    const user = userEvent.setup();
    show({ initial: { items: [entry("SELECT 1")], total: 1, failed: false } });

    toggle("SELECT 1").focus();
    await user.keyboard("{Enter}");
    expect(toggle("SELECT 1")).toHaveAttribute("aria-expanded", "true");
    expect(toggle("SELECT 1")).toHaveFocus();

    await user.keyboard(" ");
    expect(toggle("SELECT 1")).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("region", { name: t.statement })).not.toBeInTheDocument();
  });

  test("an empty statement opens to an empty block without failing", async () => {
    const user = userEvent.setup();
    show({ initial: { items: [entry("", { status: "rejected" })], total: 1, failed: false } });

    await user.click(screen.getByRole("button", { name: t.show }));

    expect(statement().querySelector("pre")).toHaveTextContent("");
    expect(within(statement()).getByRole("button", { name: t.copy })).toBeInTheDocument();
  });

  test("each row opens on its own", async () => {
    const user = userEvent.setup();
    show({ initial: { items: [entry("SELECT 1"), entry("SELECT 2")], total: 2, failed: false } });

    await user.click(toggle("SELECT 2"));

    expect(toggle("SELECT 1")).toHaveAttribute("aria-expanded", "false");
    expect(screen.getAllByRole("region", { name: t.statement })).toHaveLength(1);
  });
});
