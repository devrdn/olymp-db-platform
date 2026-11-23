import { render, screen } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// A Server Component: the API is faked, the contest's wire format is
// `contests.test.ts`'s business, and the view only records its props.
const { serverRequest, view } = vi.hoisted(() => ({
  serverRequest: vi.fn(),
  view: vi.fn<(props: Record<string, unknown>) => null>(() => null),
}));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("@/lib/api/contests", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/contests")>()),
  contestSchema: { parse: (payload: unknown) => payload },
}));
vi.mock("./monitor-view", () => ({ MonitorView: view }));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});

import MonitorPage from "./page";

const CONTEST = "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";
const base = `/contests/${CONTEST}`;

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

/** The API's answers for a contest in `status`, with `refused` standing in for a read that fails. */
function serve(status = "running", refused?: { path: string; error: unknown }) {
  serverRequest.mockImplementation(async (path: string) => {
    if (refused && path.startsWith(refused.path)) throw refused.error;
    if (path === base) return { id: CONTEST, status };
    if (path === `${base}/monitor/participants`) return { generated_at: "x", truncated: false, rows: [] };
    if (path.startsWith(`${base}/monitor/feed`)) return { items: [], more: false };
    throw new Error(`unexpected ${path}`);
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  serve();
});

async function renderPage() {
  render(
    await MonitorPage({
      params: Promise.resolve({ contestId: CONTEST }),
      searchParams: Promise.resolve({}),
    } as PageProps<"/contests/[contestId]/monitor">),
  );
}

const viewProps = () => view.mock.calls[0][0];

describe("the monitoring page", () => {
  /** Every prop of the client view is serialised into the page, so the dictionary goes as one section. */
  test("hands the screen only the dictionary section it reads", async () => {
    await renderPage();

    expect(Object.keys(viewProps().dict as object)).toEqual(["workspace"]);
    expect((viewProps().dict as Dictionary).workspace).toBe(dict.workspace);
  });

  // Opened before the start, the screen must begin watching when the contest does.
  test.each([
    { status: "published", changing: true },
    { status: "running", changing: true },
    { status: "finished", changing: false },
  ])("lets the screen poll a $status contest: $changing", async ({ status, changing }) => {
    serve(status);
    await renderPage();
    expect(viewProps().changing).toBe(changing);
  });

  test("starts from the table and the newest page of the feed", async () => {
    await renderPage();

    expect(viewProps().roster).toEqual({ generatedAt: "x", truncated: false, rows: [] });
    expect(viewProps().feed).toEqual({ items: [], more: false });
  });

  /** The organiser's own read budget is a refusal to show, not a failed load. */
  test.each(["/monitor/participants", "/monitor/feed"])(
    "says so in words when the organiser refreshes too often and %s is refused",
    async (read) => {
      serve("running", { path: `${base}${read}`, error: new ApiError("monitor_too_often", 429, "Too often") });

      await renderPage();

      expect(screen.getByRole("status")).toHaveTextContent(dict.errors.monitor_too_often);
      expect(view).not.toHaveBeenCalled();
    },
  );
});
