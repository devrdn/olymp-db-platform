import { render, screen, within } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The page is a Server Component: the API and the request are faked, and what
// is under test is what it reads for the address and what it renders.
const { serverRequest, notFound } = vi.hoisted(() => ({
  serverRequest: vi.fn(),
  notFound: vi.fn(() => {
    throw new Error("NEXT_NOT_FOUND");
  }),
}));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("next/navigation", () => ({
  notFound,
  redirect: vi.fn(),
  usePathname: () => "/",
  useRouter: () => ({ refresh: vi.fn(), push: vi.fn() }),
}));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});

import ReportPage, { generateMetadata } from "./page";

const CONTEST = "6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d01";
const base = `/me/contests/${CONTEST}`;

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

const report = {
  contest_id: CONTEST,
  title: "The Greenhouse",
  status: "finished",
  starts_at: "2026-05-14T07:00:00Z",
  ends_at: "2026-05-14T10:00:00Z",
  result: { scoring: "points", points: 60, solved: 3, state: "final", place_open: true, place: 4, participants: 31 },
  started_at: "2026-05-14T07:02:00Z",
  queries: 120,
  successful_queries: 98,
  worked_ms: 5_400_000,
  questions: [],
};

function answer(path: string): unknown {
  if (path === `${base}/report`) return report;
  if (path.startsWith(`${base}/queries`)) {
    return {
      items: [
        { cursor: "q1", executed_at: "2026-05-14T08:00:00Z", id: 1, sql: "SELECT * FROM guests", status: "ok", duration_ms: 3, row_count: 2 },
      ],
      more: false,
    };
  }
  if (path === `${base}/answers`) return { questions: [], truncated: false };
  if (path === `${base}/workspace`) return { notes: { body: "", updated_at: null }, tabs: [] };
  throw new Error(`unexpected ${path}`);
}

beforeEach(() => {
  vi.clearAllMocks();
  serverRequest.mockImplementation(async (path: string) => answer(path));
});

async function renderPage(tab?: string | string[], contestId = CONTEST) {
  render(
    await ReportPage({
      params: Promise.resolve({ contestId }),
      searchParams: Promise.resolve(tab === undefined ? {} : { tab }),
    } as unknown as Parameters<typeof ReportPage>[0]),
  );
}

const names = () => dict.profile.report.tabs;
const asked = () => serverRequest.mock.calls.map(([path]) => path as string);

describe("the heading", () => {
  test("names the contest, says when it ran, and offers the CSV and the way back", async () => {
    await renderPage();

    expect(screen.getByRole("heading", { name: "The Greenhouse" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: dict.profile.report.csvLabel })).toHaveAttribute(
      "href",
      `/api/v1${base}/log.csv`,
    );
    expect(screen.getByRole("link", { name: dict.profile.report.back })).toHaveAttribute("href", "/profile");
  });
});

/**
 * The tab is in the address, so it is in the title too: three browser tabs
 * of one report all called "Result" are three tabs nobody can tell apart.
 */
describe("the title of the browser tab", () => {
  test("names the tab the address asks for", async () => {
    expect(await generateMetadata({ searchParams: Promise.resolve({ tab: "queries" }) } as never)).toEqual({
      title: dict.profile.report.tabs.queries,
    });
    expect(await generateMetadata({ searchParams: Promise.resolve({}) } as never)).toEqual({
      title: dict.profile.report.tabs.summary,
    });
  });
});

describe("the tab in the address", () => {
  test("no tab is the result, and only the report is read", async () => {
    await renderPage();

    expect(screen.getByRole("link", { name: names().summary })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("region", { name: names().summary })).toBeInTheDocument();
    expect(asked()).toEqual([`${base}/report`]);
  });

  test("?tab=queries opens the queries and reads only what that tab needs", async () => {
    await renderPage("queries");

    expect(screen.getByRole("link", { name: names().queries })).toHaveAttribute("aria-current", "page");
    const panel = screen.getByRole("region", { name: names().queries });
    expect(within(panel).getByText("SELECT * FROM guests")).toBeInTheDocument();
    expect(asked()).toContain(`${base}/queries`);
    expect(asked().some((path) => path.endsWith("/answers"))).toBe(false);
  });

  test.each([
    ["answers", `${base}/answers`],
    ["notes", `${base}/workspace`],
  ])("?tab=%s reads its own data", async (tab, path) => {
    await renderPage(tab);

    expect(asked()).toContain(path);
    expect(screen.getByRole("region", { name: names()[tab as keyof ReturnType<typeof names>] })).toBeInTheDocument();
  });

  test("an unknown tab is the result", async () => {
    await renderPage("teleport");
    expect(screen.getByRole("link", { name: names().summary })).toHaveAttribute("aria-current", "page");
  });
});

/**
 * One answer for every one of them. A contest that is not this participant's,
 * one that has not ended for them, and one that does not exist are the same
 * 404 from the API with the same code, and the same not-found page here: the
 * profile does not say what exists or who is on it.
 */
describe("an address that leads nowhere", () => {
  test("a contest that has not finished for this participant is not found", async () => {
    serverRequest.mockImplementation(async () => {
      throw new ApiError("profile_contest_not_found", 404, "No finished contest of yours with that identifier");
    });

    await expect(renderPage()).rejects.toThrow("NEXT_NOT_FOUND");
    expect(notFound).toHaveBeenCalled();
  });

  test("somebody else's contest is the same answer, on every tab", async () => {
    serverRequest.mockImplementation(async () => {
      throw new ApiError("profile_contest_not_found", 404, "No finished contest of yours with that identifier");
    });

    await expect(renderPage("notes")).rejects.toThrow("NEXT_NOT_FOUND");
  });

  test("an identifier that is not one is not asked about at all", async () => {
    await expect(renderPage(undefined, "not-an-id")).rejects.toThrow("NEXT_NOT_FOUND");
    expect(serverRequest).not.toHaveBeenCalled();
  });

  /**
   * The report's own answer decides the page, whichever read fails first.
   *
   * The two reads run together, and a tab that fails for its own reason must
   * not be what the reader is shown when the report says the contest is not
   * theirs: a race would answer with whichever rejection arrived first, and
   * the loser's would be left with nobody to receive it.
   */
  test("answers with the report's failure even when the tab fails sooner", async () => {
    serverRequest.mockImplementation(async (path: string) => {
      if (path.startsWith(`${base}/queries`)) throw new Error("the tab read broke first");
      // A tick later, so the tab's failure is certainly the first one.
      await new Promise((resolve) => setTimeout(resolve, 0));
      throw new ApiError("profile_contest_not_found", 404, "No finished contest of yours with that identifier");
    });

    await expect(renderPage("queries")).rejects.toThrow("NEXT_NOT_FOUND");
    expect(notFound).toHaveBeenCalled();
  });
});
