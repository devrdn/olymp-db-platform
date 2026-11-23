import { render, screen } from "@testing-library/react";
import { afterEach, beforeAll, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// A Server Component: the API is faked, and the wire formats are the schema
// tests' business, so each answer passes through as it was sent.
const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("@/lib/api/contests", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/contests")>()),
  contestSchema: { parse: (payload: unknown) => payload },
}));
vi.mock("@/lib/api/game", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/game")>()),
  gameSchema: { parse: (payload: unknown) => payload },
}));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});
vi.mock("./game-databases", () => ({ GameDatabases: () => <p data-testid="databases" /> }));

import DatabasesPage from "./page";

const CONTEST = "f767af3b-f135-40d2-a3a6-82d368de1004";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("the contest's databases, when the cluster cannot be read", () => {
  test("says so, and logs the failure on one line without what it said", async () => {
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});
    serverRequest.mockImplementation(async (path: string) => {
      if (path === `/contests/${CONTEST}/game/instances`) {
        throw new TypeError('Headers.append: "dbcontest_session=secret-token\nFAKE LINE" is an invalid header value.');
      }
      if (path === `/contests/${CONTEST}/game`) return { status: "ready" };
      return { id: CONTEST, status: "running" };
    });

    render(
      await DatabasesPage({
        params: Promise.resolve({ contestId: CONTEST }),
      } as unknown as PageProps<"/contests/[contestId]/databases">),
    );

    expect(screen.getByRole("alert")).toHaveTextContent(dict.workspace.databases.unreachable);
    expect(logged).toHaveBeenCalledTimes(1);
    const line = logged.mock.calls[0].map(String).join(" ");
    expect(line).toContain("/game/instances");
    expect(line).not.toContain("secret-token");
    expect(line).not.toMatch(/[\r\n]/);
  });
});
