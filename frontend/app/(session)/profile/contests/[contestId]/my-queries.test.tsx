import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { loggedQuery } from "@/components/product/test-fixtures";
import { SEARCH_DEBOUNCE_MS } from "@/components/product/use-query-log";
import type { QueriesPage } from "@/lib/api/journal";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

const { fetchMyQueries } = vi.hoisted(() => ({ fetchMyQueries: vi.fn() }));
vi.mock("@/lib/api/profile", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/profile")>()),
  fetchMyQueries,
}));

import { MyQueries } from "./my-queries";

const CONTEST = "6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d01";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

beforeEach(() => {
  vi.useFakeTimers();
  vi.resetAllMocks();
  fetchMyQueries.mockResolvedValue({ items: [], more: false });
});

afterEach(() => {
  vi.useRealTimers();
});

const t = () => dict.profile.report.queries;

function renderTab(initial: QueriesPage = { items: [loggedQuery(9), loggedQuery(8)], more: true }) {
  return render(
    <MyQueries
      contestId={CONTEST}
      initial={initial}
      t={dict.profile.report}
      statuses={dict.participant.play.workspace.log.status}
      locale="en"
    />,
  );
}

describe("my queries", () => {
  test("lists them newest first", () => {
    renderTab();
    const list = screen.getByRole("list", { name: dict.profile.report.tabs.queries });
    expect(within(list).getAllByRole("listitem")).toHaveLength(2);
    expect(within(list).getByText("SELECT 9")).toBeInTheDocument();
  });

  /**
   * The participant's own address is not shown: it is theirs, it explains
   * nothing to them, and it is in the way (design §2.2).
   */
  test("shows no address, not even an empty one", () => {
    renderTab({ items: [loggedQuery(9, { ip: "10.0.0.1" })], more: false });

    expect(screen.queryByText("10.0.0.1")).not.toBeInTheDocument();
    expect(screen.queryByText(/no address/i)).not.toBeInTheDocument();
  });

  test("filters by status, under the caller's own route", async () => {
    fetchMyQueries.mockResolvedValueOnce({ items: [loggedQuery(4, { status: "error", error: "boom" })], more: false });
    renderTab();

    await act(async () => {
      fireEvent.change(screen.getByLabelText(t().status), { target: { value: "error" } });
    });
    expect(fetchMyQueries).toHaveBeenCalledWith(CONTEST, { status: "error", q: "" }, expect.anything());
    expect(screen.getByText("boom")).toBeInTheDocument();
  });

  test("searches once the typing stops", async () => {
    renderTab();

    fireEvent.change(screen.getByLabelText(t().search), { target: { value: "guests" } });
    expect(fetchMyQueries).not.toHaveBeenCalled();
    await act(() => vi.advanceTimersByTimeAsync(SEARCH_DEBOUNCE_MS));

    expect(fetchMyQueries).toHaveBeenCalledWith(CONTEST, { status: "", q: "guests" }, expect.anything());
    expect(screen.getByText(t().noMatch)).toBeInTheDocument();
  });

  test("loads more after the last query held", async () => {
    fetchMyQueries.mockResolvedValueOnce({ items: [loggedQuery(7)], more: false });
    renderTab();

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: t().loadMore }));
    });
    expect(fetchMyQueries).toHaveBeenCalledWith(CONTEST, { status: "", q: "", cursor: "q8" }, expect.anything());
    expect(screen.getAllByRole("listitem")).toHaveLength(3);
    expect(screen.queryByRole("button", { name: t().loadMore })).not.toBeInTheDocument();
  });

  test("expands to the whole statement and copies it", async () => {
    const writeText = vi.fn(async () => {});
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    renderTab({ items: [loggedQuery(1, { sql: "SELECT name\nFROM guests" })], more: false });

    fireEvent.click(screen.getByRole("button", { name: t().show }));
    const code = document.querySelector("pre code") as HTMLElement;
    expect(code.textContent).toBe("SELECT name\nFROM guests");

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: t().copy }));
    });
    expect(writeText).toHaveBeenCalledWith("SELECT name\nFROM guests");
    vi.unstubAllGlobals();
  });

  test("says when there is nothing at all", () => {
    renderTab({ items: [], more: false });
    expect(screen.getByText(t().empty)).toBeInTheDocument();
  });
});
