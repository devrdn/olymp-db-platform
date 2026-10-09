import { render, screen, within } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// A Server Component: API and request are faked.
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

import ParticipantPage from "./page";
import { CONTEST, REG } from "./test-fixtures";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

const flags = {
  multiple_ips: true,
  parallel_sessions: false,
  long_absence: false,
  answer_without_queries: false,
  large_paste: false,
  identical_queries: false,
};

const rosterRow = {
  registration_id: REG,
  login: "ivanov",
  full_name: "Ivan Ivanov",
  status: "finished",
  started_at: "2026-09-20T09:00:00.000Z",
  finished_at: "2026-09-20T11:00:00.000Z",
  queries: 1,
  query_errors: 0,
  query_rejected: 0,
  addresses: 2,
  correct: 0,
  wrong: 0,
  page_left: 0,
  away_ms: 0,
  pastes: 0,
  ip_changes: 1,
  parallel_sessions: 0,
  last_activity: null,
  flags,
};

const base = `/contests/${CONTEST}/monitor/participants/${REG}`;

function answer(path: string): unknown {
  if (path === `/contests/${CONTEST}/monitor/participants`) {
    return { generated_at: "x", truncated: false, rows: [rosterRow] };
  }
  if (path === base) {
    const { registration_id, login, full_name, status, started_at, finished_at } = rosterRow;
    return { registration_id, login, full_name, status, started_at, finished_at };
  }
  if (path.startsWith(`${base}/timeline`)) return { items: [], more: false };
  if (path.startsWith(`${base}/queries`)) {
    return {
      items: [
        {
          cursor: "q1",
          executed_at: "2026-09-20T10:00:00.000Z",
          id: 1,
          sql: "SELECT * FROM guests",
          status: "ok",
          duration_ms: 3,
          row_count: 2,
          ip: "10.0.0.1",
        },
      ],
      more: false,
    };
  }
  if (path === `${base}/answers`) return { questions: [], truncated: false };
  if (path === `${base}/workspace`) {
    return { notes: { body: "", updated_at: null }, tabs: [], revisions: [], truncated: false };
  }
  throw new Error(`unexpected ${path}`);
}

beforeEach(() => {
  vi.clearAllMocks();
  serverRequest.mockImplementation(async (path: string) => answer(path));
});

async function renderPage(tab?: string | string[], registrationId = REG) {
  render(
    await ParticipantPage({
      params: Promise.resolve({ contestId: CONTEST, registrationId }),
      searchParams: Promise.resolve(tab === undefined ? {} : { tab }),
    } as unknown as Parameters<typeof ParticipantPage>[0]),
  );
}

const names = () => dict.workspace.monitor.participant.tabs;
const asked = () => serverRequest.mock.calls.map(([path]) => path as string);

describe("the heading", () => {
  test("says who, their status and clock, their flags, and offers their CSV and the way back", async () => {
    await renderPage();

    expect(screen.getByRole("heading", { name: "Ivan Ivanov" })).toBeInTheDocument();
    expect(screen.getByText("ivanov")).toBeInTheDocument();
    expect(screen.getByText(dict.workspace.people.registration.finished)).toBeInTheDocument();
    expect(screen.getByText(dict.workspace.monitor.flags.multipleIps.label)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: dict.workspace.monitor.participant.csvLabel })).toHaveAttribute(
      "href",
      `/api/v1${base}/export.csv`,
    );
    expect(screen.getByRole("link", { name: dict.workspace.monitor.participant.back })).toHaveAttribute(
      "href",
      `/contests/${CONTEST}/monitor`,
    );
  });
});

describe("the tab in the address", () => {
  test("no tab is the timeline, read first on the server", async () => {
    await renderPage();

    expect(screen.getByRole("link", { name: names().timeline })).toHaveAttribute("aria-current", "page");
    expect(asked()).toContain(`${base}/timeline?limit=200`);
    expect(screen.getByRole("region", { name: names().timeline })).toBeInTheDocument();
  });

  test("?tab=queries opens the queries, and reads only what that tab needs", async () => {
    await renderPage("queries");

    expect(screen.getByRole("link", { name: names().queries })).toHaveAttribute("aria-current", "page");
    const panel = screen.getByRole("region", { name: names().queries });
    expect(within(panel).getByText("SELECT * FROM guests")).toBeInTheDocument();
    expect(asked()).toContain(`${base}/queries`);
    expect(asked().some((path) => path.includes("/timeline"))).toBe(false);
  });

  test.each([
    ["answers", `${base}/answers`],
    ["workspace", `${base}/workspace`],
    ["sessions", `${base}/timeline?kinds=sign_in%2Csign_out%2Csign_in_failed%2Cip_changed%2Cparallel_session&limit=200`],
  ])("?tab=%s reads its own data", async (tab, path) => {
    await renderPage(tab);
    expect(asked()).toContain(path);
    expect(screen.getByRole("region", { name: names()[tab as keyof ReturnType<typeof names>] })).toBeInTheDocument();
  });

  test("an unknown tab is the timeline", async () => {
    await renderPage("teleport");
    expect(screen.getByRole("link", { name: names().timeline })).toHaveAttribute("aria-current", "page");
  });
});

describe("an address that leads nowhere", () => {
  /**
   * Another contest's registration is a 404 like a missing one, so the
   * not-found page, not a retry.
   */
  test("another contest's registration is not found", async () => {
    serverRequest.mockImplementation(async (path: string) => {
      if (path.startsWith(base)) {
        throw new ApiError("monitor_participant_not_found", 404, "No such participant in this contest");
      }
      return answer(path);
    });

    await expect(renderPage()).rejects.toThrow("NEXT_NOT_FOUND");
    expect(notFound).toHaveBeenCalled();
  });

  test("a registration that is not an identifier is not asked about at all", async () => {
    await expect(renderPage(undefined, "not-an-id")).rejects.toThrow("NEXT_NOT_FOUND");
    expect(serverRequest).not.toHaveBeenCalled();
  });
});
