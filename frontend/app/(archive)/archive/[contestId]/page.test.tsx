import { render, screen } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

const { serverRequest, branding, notFound, redirect, standings } = vi.hoisted(() => ({
  serverRequest: vi.fn(),
  branding: vi.fn(),
  notFound: vi.fn(() => {
    throw new Error("NEXT_NOT_FOUND");
  }),
  redirect: vi.fn((target: string) => {
    throw new Error(`NEXT_REDIRECT ${target}`);
  }),
  standings: vi.fn(),
}));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("@/lib/api/branding", () => ({ branding }));
vi.mock("next/navigation", () => ({ notFound, redirect }));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});
// The table is a client component that polls; this page only hands it the standings.
vi.mock("@/components/product/public-standings", () => ({
  PublicStandings: (props: { contestId: string; initial?: { title: string }; placeholder?: React.ReactNode }) => {
    standings(props);
    return props.initial ? <p data-testid="table">{props.initial.title}</p> : <>{props.placeholder}</>;
  },
}));

import ArchiveContestPage, { generateMetadata } from "./page";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

const ID = "11111111-1111-4111-8111-111111111111";

const standingsBody = {
  state: "final",
  scoring: "points",
  title: "SQL Joins Cup",
  generated_at: "2026-09-20T12:00:00Z",
  truncated: false,
  rows: [],
};

function contest(overrides: Record<string, unknown> = {}) {
  return {
    id: ID,
    title: "SQL Joins Cup",
    description: "A day of joins.",
    status: "archived",
    starts_at: "2026-09-20T09:00:00Z",
    ends_at: "2026-09-20T11:00:00Z",
    participants: 1,
    ...overrides,
  };
}

function serve(archived: unknown, table: unknown = standingsBody) {
  serverRequest.mockImplementation(async (path: string) => {
    if (path.startsWith(`/archive/contests/${ID}`)) {
      if (archived instanceof Error) throw archived;
      return archived;
    }
    if (path.startsWith(`/contests/${ID}/leaderboard`)) {
      if (table instanceof Error) throw table;
      return table;
    }
    throw new Error(`unexpected ${path}`);
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  branding.mockResolvedValue({ archive: true });
});

async function renderPage(contestId = ID) {
  render(await ArchiveContestPage({ params: Promise.resolve({ contestId }), searchParams: Promise.resolve({}) } as PageProps<"/archive/[contestId]">));
}

describe("a contest in the archive", () => {
  test("renders not found while the archive is off", async () => {
    branding.mockResolvedValue({ archive: false });
    await expect(renderPage()).rejects.toThrow("NEXT_NOT_FOUND");
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("renders not found for an id the API does not list", async () => {
    serve(new ApiError("not_found", 404, "x"));
    await expect(renderPage()).rejects.toThrow("NEXT_NOT_FOUND");
  });

  test("sends a dead session to sign-in, back to this contest", async () => {
    serve(new ApiError("unauthenticated", 401, "x"));
    vi.spyOn(console, "warn").mockImplementation(() => {});
    await expect(renderPage()).rejects.toThrow(`NEXT_REDIRECT /login?next=${encodeURIComponent(`/archive/${ID}`)}`);
  });

  test("renders not found for a malformed id without asking the API", async () => {
    await expect(renderPage("../auth/logout")).rejects.toThrow("NEXT_NOT_FOUND");
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("shows the card: title, status, participants and a way back", async () => {
    serve(contest());
    await renderPage();

    expect(screen.getByRole("heading", { level: 1, name: "SQL Joins Cup" })).toBeInTheDocument();
    expect(screen.getByText("A day of joins.")).toBeInTheDocument();
    expect(screen.getByText("1 participant")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: en.archive.back })).toHaveAttribute("href", "/archive");
    expect(serverRequest).toHaveBeenCalledWith(`/archive/contests/${ID}?lang=en`);
  });

  test("says a finished contest's results can still change", async () => {
    serve(contest({ status: "finished" }));
    await renderPage();
    expect(screen.getByText(en.archive.provisional)).toBeInTheDocument();
  });

  test("says nothing of the sort about an archived one", async () => {
    serve(contest({ status: "archived" }));
    await renderPage();
    expect(screen.queryByText(en.archive.provisional)).not.toBeInTheDocument();
  });

  test("shows the public table under #results", async () => {
    serve(contest());
    await renderPage();

    const section = screen.getByRole("heading", { name: en.archive.results }).closest("section");
    expect(section).toHaveAttribute("id", "results");
    expect(section).toContainElement(screen.getByTestId("table"));
    expect(standings).toHaveBeenCalledWith(expect.objectContaining({ contestId: ID }));
    expect(serverRequest).toHaveBeenCalledWith(`/contests/${ID}/leaderboard?lang=en`);
  });

  test("has no tasks section unless the API sent tasks", async () => {
    serve(contest());
    await renderPage();
    expect(screen.queryByRole("heading", { name: en.archive.tasks.heading })).not.toBeInTheDocument();
  });

  test("shows the tasks when the API sent them", async () => {
    serve(
      contest({
        tasks: { lang: "en", story_md: "Once upon a table.", schema_notes_md: "", questions: [] },
      }),
    );
    await renderPage();
    expect(screen.getByRole("heading", { name: en.archive.tasks.heading })).toBeInTheDocument();
    expect(screen.getByText("Once upon a table.")).toBeInTheDocument();
  });

  test("says so, in words, when the archive is opened too often", async () => {
    serve(new ApiError("archive_too_often", 429, "x"));
    await renderPage();
    expect(screen.getByRole("status")).toHaveTextContent(en.errors.archive_too_often);
    expect(screen.queryByTestId("table")).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { level: 1 })).not.toBeInTheDocument();
  });

  test("a refused table still shows the card and the tasks, and says so under #results", async () => {
    serve(
      contest({ tasks: { lang: "en", story_md: "Once upon a table.", schema_notes_md: "", questions: [] } }),
      new ApiError("leaderboard_too_often", 429, "x"),
    );
    await renderPage();

    expect(screen.getByRole("heading", { level: 1, name: "SQL Joins Cup" })).toBeInTheDocument();
    expect(screen.getByText("Once upon a table.")).toBeInTheDocument();
    const section = screen.getByRole("heading", { name: en.archive.results }).closest("section");
    expect(section).toHaveAttribute("id", "results");
    expect(section).toContainElement(screen.getByRole("status"));
    expect(screen.getByRole("status")).toHaveTextContent(en.errors.leaderboard_too_often);
    // The browser asks again from the visitor's own address.
    expect(standings).toHaveBeenCalledWith(expect.objectContaining({ contestId: ID, initial: undefined }));
  });

  test("a table that cannot be read at all still shows the card", async () => {
    serve(contest(), new Error("socket hang up"));
    await renderPage();

    expect(screen.getByRole("heading", { level: 1, name: "SQL Joins Cup" })).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent(en.errors.unreachable);
  });

  test("keeps the page out of search engines without asking the API anything", async () => {
    serve(contest());
    const meta = await generateMetadata();
    expect(meta).toMatchObject({ title: en.archive.heading, robots: { index: false, follow: false } });
    expect(serverRequest).not.toHaveBeenCalled();
    expect(branding).not.toHaveBeenCalled();
  });
});
