import { render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";

// A Server Component: the API is faked, and the wire formats are the schema
// tests' business, so each answer passes through as it was sent.
const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("@/lib/api/contests", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/contests")>()),
  contestSchema: { parse: (payload: unknown) => payload },
}));
vi.mock("@/lib/api/game", async (importOriginal) => {
  const asIs = { parse: (payload: unknown) => payload };
  return {
    ...(await importOriginal<typeof import("@/lib/api/game")>()),
    gameSchema: asIs,
    gameScriptSchema: asIs,
    uploadSchema: asIs,
    definitionSchema: asIs,
    tableRowWindowSchema: asIs,
    tableDataSchema: asIs,
  };
});
// The panels are their own tests' business; the builder's props are what this
// page decides from its reads.
const { builder } = vi.hoisted(() => ({ builder: vi.fn() }));
vi.mock("./game-builder", () => ({
  GameBuilder: (props: unknown) => {
    builder(props);
    return null;
  },
}));
vi.mock("./game-editor", () => ({ GameEditor: () => null }));
vi.mock("./game-upload", () => ({ GameUpload: () => null }));
vi.mock("./game-build", () => ({ GameBuild: () => null }));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});

import GamePage from "./page";

const CONTEST = "f767af3b-f135-40d2-a3a6-82d368de1004";
const base = `/contests/${CONTEST}`;

/** The API's answers, with `refused` standing in for the one read that fails. */
function serve(refused: string, error: unknown) {
  serverRequest.mockImplementation(async (path: string) => {
    if (path.startsWith(refused)) throw error;
    if (path === base) return { id: CONTEST, status: "draft" };
    if (path === `${base}/game`) return { status: "absent", uploadLimits: { enabled: true } };
    if (path === `${base}/game/script`) return { script: "" };
    if (path === `${base}/game/uploads/current`) return { status: "absent" };
    if (path === `${base}/game/definition`) {
      return { builderLimits: { enabled: true }, tables: [{ name: "guests" }] };
    }
    if (path.endsWith("/data/window?max_rows=1")) return { totalRows: 3 };
    if (path.endsWith("/data/current")) return { status: "absent" };
    throw new Error(`unexpected ${path}`);
  });
}

function page() {
  return GamePage({ params: Promise.resolve({ contestId: CONTEST }) } as unknown as PageProps<"/contests/[contestId]/game">);
}

beforeEach(() => {
  serverRequest.mockReset();
  builder.mockReset();
  vi.spyOn(console, "warn").mockImplementation(() => {});
  vi.spyOn(console, "error").mockImplementation(() => {});
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("the game page's best-effort reads", () => {
  const signIn = { digest: expect.stringContaining(`/login?next=${encodeURIComponent(base)}`) };

  test.each([
    ["the current upload", `${base}/game/uploads/current`],
    ["the table builder's definition", `${base}/game/definition`],
    ["a table's row count", `${base}/game/tables/guests/data/window`],
    ["a table's current upload", `${base}/game/tables/guests/data/current`],
  ])("send a session that died before %s was read to sign-in", async (_name, refused) => {
    serve(refused, new ApiError("unauthenticated", 401, "Sign in"));

    await expect(page()).rejects.toMatchObject(signIn);
  });

  test("render without a read that failed for any other reason, and log it", async () => {
    serve(`${base}/game/tables/guests/data/window`, new ApiError("internal_error", 500, "", "req-9"));

    render(await page());

    expect(builder).toHaveBeenCalledWith(
      expect.objectContaining({ rowCounts: { guests: 0 }, currentTableUploads: { guests: { status: "absent" } } }),
    );
    expect(console.error).toHaveBeenCalledWith(expect.stringContaining("req-9"));
  });
});

describe("the game page's per-table reads", () => {
  test("ask nothing for a table whose name is no path segment", async () => {
    serve("nothing refused", null);
    const answer = serverRequest.getMockImplementation()!;
    serverRequest.mockImplementation(async (path: string) =>
      path === `${base}/game/definition` ? { builderLimits: { enabled: true }, tables: [{ name: ".." }] } : answer(path),
    );

    render(await page());

    expect(serverRequest.mock.calls.map(([path]) => String(path)).filter((path) => path.includes("/tables/"))).toEqual([]);
    expect(builder).toHaveBeenCalledWith(
      expect.objectContaining({ rowCounts: { "..": 0 }, currentTableUploads: { "..": null } }),
    );
  });
});
