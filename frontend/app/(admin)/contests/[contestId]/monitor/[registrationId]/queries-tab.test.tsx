import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { MAX_QUERY_SEARCH, type QueriesPage } from "@/lib/api/monitor";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

const { fetchQueries, fetchTimeline } = vi.hoisted(() => ({ fetchQueries: vi.fn(), fetchTimeline: vi.fn() }));
vi.mock("@/lib/api/monitor", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/monitor")>()),
  fetchQueries,
  fetchTimeline,
}));

import { QueriesTab } from "./queries-tab";
import { CONTEST, loggedQuery, REG, setVisibility } from "./test-fixtures";
import { SEARCH_DEBOUNCE_MS } from "./use-queries";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

beforeEach(() => {
  vi.useFakeTimers();
  vi.resetAllMocks();
  setVisibility("visible");
  fetchQueries.mockResolvedValue({ items: [], more: false });
  fetchTimeline.mockResolvedValue({ items: [], more: false });
});

afterEach(() => {
  vi.useRealTimers();
});

function renderTab(initial: QueriesPage = { items: [loggedQuery(9), loggedQuery(8)], more: true }) {
  return render(<QueriesTab contestId={CONTEST} registrationId={REG} initial={initial} dict={dict} locale="en" />);
}

const t = () => dict.workspace.monitor.participant.queries;

describe("the queries tab", () => {
  test("lists the queries, newest first", () => {
    renderTab();
    const list = screen.getByRole("list", { name: dict.workspace.monitor.participant.tabs.queries });
    expect(within(list).getAllByRole("listitem")).toHaveLength(2);
    expect(within(list).getByText("SELECT 9")).toBeInTheDocument();
  });

  test("filters by status", async () => {
    fetchQueries.mockResolvedValueOnce({ items: [loggedQuery(4, { status: "error", error: "boom" })], more: false });
    renderTab();

    await act(async () => {
      fireEvent.change(screen.getByLabelText(t().status), { target: { value: "error" } });
    });
    expect(fetchQueries).toHaveBeenCalledWith(CONTEST, REG, { status: "error", q: "" }, expect.anything());
    expect(screen.getByText("boom")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t().loadMore })).not.toBeInTheDocument();
  });

  test("searches the text once the typing stops, never past the bound", async () => {
    renderTab();
    const search = screen.getByLabelText(t().search);
    expect(search).toHaveAttribute("maxLength", String(MAX_QUERY_SEARCH));

    fireEvent.change(search, { target: { value: "guests" } });
    expect(fetchQueries).not.toHaveBeenCalled();
    await act(() => vi.advanceTimersByTimeAsync(SEARCH_DEBOUNCE_MS));
    expect(fetchQueries).toHaveBeenCalledWith(CONTEST, REG, { status: "", q: "guests" }, expect.anything());
    expect(screen.getByText(t().noMatch)).toBeInTheDocument();
  });

  test("loads more after the last query", async () => {
    fetchQueries.mockResolvedValueOnce({ items: [loggedQuery(7)], more: false });
    renderTab();

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: t().loadMore }));
    });
    expect(fetchQueries).toHaveBeenCalledWith(CONTEST, REG, { status: "", q: "", cursor: "q8" }, expect.anything());
    expect(screen.getAllByRole("listitem")).toHaveLength(3);
    expect(screen.queryByRole("button", { name: t().loadMore })).not.toBeInTheDocument();
  });

  test("says when there is nothing at all", () => {
    renderTab({ items: [], more: false });
    expect(screen.getByText(t().empty)).toBeInTheDocument();
  });
});
