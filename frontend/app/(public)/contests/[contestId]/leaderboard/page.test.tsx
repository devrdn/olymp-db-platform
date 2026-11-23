import { render, screen } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

const { serverRequest, notFound } = vi.hoisted(() => ({
  serverRequest: vi.fn(),
  notFound: vi.fn(() => {
    throw new Error("NEXT_NOT_FOUND");
  }),
}));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("next/navigation", () => ({ notFound, redirect: vi.fn() }));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});
// The table is a client component that polls; this page only hands it the standings.
vi.mock("@/components/product/public-standings", () => ({
  PublicStandings: (props: { initial: { title: string } }) => <p data-testid="table">{props.initial.title}</p>,
}));

import PublicLeaderboardPage from "./page";

const ID = "11111111-1111-4111-8111-111111111111";

let en: Dictionary;
beforeAll(async () => {
  en = await getDictionary("en");
});

beforeEach(() => {
  vi.clearAllMocks();
});

async function renderPage(contestId = ID) {
  render(
    await PublicLeaderboardPage({
      params: Promise.resolve({ contestId }),
    } as unknown as PageProps<"/contests/[contestId]/leaderboard">),
  );
}

describe("a contest's public table", () => {
  test("names the contest and hands the standings to the table", async () => {
    serverRequest.mockResolvedValue({
      state: "final",
      scoring: "points",
      title: "SQL Joins Cup",
      generated_at: "2026-09-20T12:00:00Z",
      truncated: false,
      rows: [],
    });

    await renderPage();

    expect(screen.getByRole("heading", { level: 1, name: "SQL Joins Cup" })).toBeInTheDocument();
    expect(serverRequest).toHaveBeenCalledWith(`/contests/${ID}/leaderboard?lang=en`);
  });

  test("is not found for a contest the API does not know", async () => {
    serverRequest.mockRejectedValue(new ApiError("not_found", 404, "Resource not found"));
    await expect(renderPage()).rejects.toThrow("NEXT_NOT_FOUND");
  });

  test("is a failed load, not a missing contest, when a gateway's 404 page answered instead of the API", async () => {
    serverRequest.mockRejectedValue(new ApiError("unreachable", 404, "Unparseable response from the API"));
    await expect(renderPage()).rejects.toMatchObject({ code: "unreachable" });
    expect(notFound).not.toHaveBeenCalled();
  });

  test("says so, in words, when the table is asked for too often", async () => {
    serverRequest.mockRejectedValue(new ApiError("leaderboard_too_often", 429, "x"));

    await renderPage();

    expect(screen.getByRole("status")).toHaveTextContent(en.errors.leaderboard_too_often);
    expect(screen.queryByTestId("table")).not.toBeInTheDocument();
  });

  test("is not found for a malformed id without asking the API", async () => {
    await expect(renderPage("../auth/logout")).rejects.toThrow("NEXT_NOT_FOUND");
    expect(serverRequest).not.toHaveBeenCalled();
  });
});
