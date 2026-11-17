import { render, screen, within } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

const { serverRequest, branding, notFound, redirect } = vi.hoisted(() => ({
  serverRequest: vi.fn(),
  branding: vi.fn(),
  notFound: vi.fn(() => {
    throw new Error("NEXT_NOT_FOUND");
  }),
  redirect: vi.fn((target: string) => {
    throw new Error(`NEXT_REDIRECT ${target}`);
  }),
}));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("@/lib/api/branding", () => ({ branding }));
vi.mock("next/navigation", () => ({ notFound, redirect }));
// An anchor that shows what the page asked of prefetching, which the real Link
// keeps to itself.
vi.mock("next/link", () => ({
  default: ({ prefetch, children, ...rest }: React.ComponentProps<"a"> & { prefetch?: boolean | null }) => (
    <a data-prefetch={String(prefetch)} {...rest}>
      {children}
    </a>
  ),
}));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});

import ArchivePage, { generateMetadata } from "./page";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

beforeEach(() => {
  vi.clearAllMocks();
  branding.mockResolvedValue({ archive: true });
});

const ID = "11111111-1111-4111-8111-111111111111";

function row(overrides: Record<string, unknown> = {}) {
  return {
    id: ID,
    title: "SQL Joins Cup",
    status: "archived",
    starts_at: "2026-09-20T09:00:00Z",
    ends_at: "2026-09-20T11:00:00Z",
    participants: 12,
    ...overrides,
  };
}

function answer(body: unknown) {
  serverRequest.mockResolvedValue(body);
}

async function renderPage(search: Record<string, string | string[]> = {}) {
  render(await ArchivePage({ params: Promise.resolve({}), searchParams: Promise.resolve(search) } as PageProps<"/archive">));
}

describe("the archive list", () => {
  test("renders not found while the archive is off", async () => {
    branding.mockResolvedValue({ archive: false });
    await expect(renderPage()).rejects.toThrow("NEXT_NOT_FOUND");
    expect(notFound).toHaveBeenCalled();
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("renders not found when the API says the archive is off", async () => {
    serverRequest.mockRejectedValue(new ApiError("not_found", 404, "x"));
    await expect(renderPage()).rejects.toThrow("NEXT_NOT_FOUND");
  });

  test("lists each contest with its window, status, participants and a results link", async () => {
    answer({ items: [row()], total: 1 });
    await renderPage();

    const title = screen.getByRole("link", { name: "SQL Joins Cup" });
    expect(title).toHaveAttribute("href", `/archive/${ID}`);
    const results = screen.getByRole("link", { name: "Results of SQL Joins Cup" });
    expect(results).toHaveAttribute("href", `/archive/${ID}#results`);
    expect(screen.getByText("archived")).toBeInTheDocument();
    expect(screen.getByText("12 participants")).toBeInTheDocument();
    const tableRow = screen.getByRole("row", { name: /SQL Joins Cup/ });
    expect(within(tableRow).getByText(/2026/)).toBeInTheDocument();
  });

  test("does not prefetch a contest's page, whose every read spends the reader's budget", async () => {
    answer({ items: [row()], total: 1 });
    await renderPage();

    expect(screen.getByRole("link", { name: "SQL Joins Cup" })).toHaveAttribute("data-prefetch", "false");
    expect(screen.getByRole("link", { name: "Results of SQL Joins Cup" })).toHaveAttribute("data-prefetch", "false");
  });

  test("names the results link with the title as written, replacement patterns and all", async () => {
    answer({ items: [row({ title: "Cup $& $1 $$" })], total: 1 });
    await renderPage();
    expect(screen.getByRole("link", { name: "Results of Cup $& $1 $$" })).toBeInTheDocument();
  });

  test("asks the API for the page the address names, in the visitor's language", async () => {
    answer({ items: [row()], total: 45 });
    await renderPage({ q: " joins ", page: "2" });

    expect(serverRequest).toHaveBeenCalledWith("/archive/contests?q=joins&limit=20&offset=20&lang=en");
  });

  test("keeps the search in the pagination links", async () => {
    answer({ items: [row()], total: 45 });
    await renderPage({ q: "joins", page: "2" });

    expect(screen.getByRole("link", { name: en.archive.pagination.previous })).toHaveAttribute(
      "href",
      "/archive?q=joins&page=1",
    );
    expect(screen.getByRole("link", { name: en.archive.pagination.next })).toHaveAttribute(
      "href",
      "/archive?q=joins&page=3",
    );
    expect(screen.getByText("21–21 of 45")).toBeInTheDocument();
  });

  test("has no next link on the last page and no previous link on the first", async () => {
    answer({ items: Array.from({ length: 5 }, (_, i) => row({ id: `id-${i}` })), total: 45 });
    await renderPage({ page: "3" });
    expect(screen.queryByRole("link", { name: en.archive.pagination.next })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: en.archive.pagination.previous })).toHaveAttribute("href", "/archive?page=2");
  });

  test("the first page has nothing before it", async () => {
    answer({ items: [row()], total: 45 });
    await renderPage();
    expect(screen.queryByRole("link", { name: en.archive.pagination.previous })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: en.archive.pagination.next })).toHaveAttribute("href", "/archive?page=2");
  });

  test("the search field takes at most 100 characters", async () => {
    answer({ items: [row()], total: 1 });
    await renderPage({ q: "abc" });

    const field = screen.getByRole("searchbox", { name: en.archive.search.label });
    expect(field).toHaveAttribute("name", "q");
    expect(field).toHaveAttribute("maxlength", "100");
    expect(field).toHaveValue("abc");
    expect(field.closest("form")).toHaveAttribute("method", "get");
    expect(field.closest("form")).toHaveAttribute("action", "/archive");
  });

  test("shows the empty state when the archive holds nothing", async () => {
    answer({ items: [], total: 0 });
    await renderPage();

    expect(screen.getByRole("heading", { name: en.archive.empty.title })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: en.archive.search.reset })).not.toBeInTheDocument();
  });

  test("a page past the end offers the first page, not the empty archive", async () => {
    answer({ items: [], total: 45 });
    await renderPage({ q: "joins", page: "9" });

    expect(screen.getByRole("heading", { name: en.archive.pastEnd.title })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: en.archive.pastEnd.first })).toHaveAttribute(
      "href",
      "/archive?q=joins&page=1",
    );
    expect(screen.queryByRole("heading", { name: en.archive.empty.title })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: en.archive.noMatch.title })).not.toBeInTheDocument();
  });

  test("shows a reset when a search found nothing", async () => {
    answer({ items: [], total: 0 });
    await renderPage({ q: "zzz" });

    expect(screen.getByRole("heading", { name: en.archive.noMatch.title })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: en.archive.search.reset })).toHaveAttribute("href", "/archive");
  });

  test("says so, in words, when the archive is opened too often", async () => {
    serverRequest.mockRejectedValue(new ApiError("archive_too_often", 429, "x"));
    await renderPage();

    expect(screen.getByRole("status")).toHaveTextContent(en.errors.archive_too_often);
  });

  test("sends a signed-out visitor to sign-in, back to the page they asked for", async () => {
    serverRequest.mockRejectedValue(new ApiError("unauthenticated", 401, "x"));
    vi.spyOn(console, "warn").mockImplementation(() => {});
    await expect(renderPage({ q: "a b" })).rejects.toThrow("NEXT_REDIRECT /login?next=%2Farchive%3Fq%3Da%2Bb");
  });

  test("keeps the page out of search engines", async () => {
    expect(await generateMetadata()).toMatchObject({ robots: { index: false, follow: false } });
  });
});
