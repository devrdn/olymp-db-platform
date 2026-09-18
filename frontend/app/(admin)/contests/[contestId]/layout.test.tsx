import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, test, vi } from "vitest";

import type { Contest } from "@/lib/api/contests";

// The layout is a Server Component: everything it reads through the request
// (the locale, the API) is faked here, and what is under test is what it
// decides from those answers — which sections it offers.
const { loadContest, loadContestResource } = vi.hoisted(() => ({
  loadContest: vi.fn(),
  loadContestResource: vi.fn(),
}));
vi.mock("./contest", () => ({ loadContest, loadContestResource }));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});
vi.mock("next/navigation", () => ({
  usePathname: () => "/contests/c1",
  useSelectedLayoutSegment: () => null,
  useRouter: () => ({ refresh: vi.fn(), push: vi.fn() }),
}));
// The title editor carries a Server Action; it is beside the point here.
vi.mock("./title-editor", () => ({ TitleEditor: () => null }));

import ContestLayout from "./layout";

const CONTEST_ID = "f767af3b-f135-40d2-a3a6-82d368de1004";

const contest = {
  id: CONTEST_ID,
  status: "running",
  enrollment: "invite_only",
  questionMode: "multi",
  timing: "fixed",
  startsAt: "2026-11-08T17:00:00Z",
  endsAt: "2026-11-08T21:30:00Z",
  allowedCidrs: [],
  settings: { queryRateLimitPerMin: 0, gracePeriodMin: 0 },
  languages: [{ code: "en", isDefault: true }],
  translations: { en: { title: "Night in the archive" } },
  createdAt: "2026-08-01T10:00:00Z",
  updatedAt: "2026-08-31T17:00:00Z",
} as unknown as Contest;

async function renderLayout() {
  render(
    await ContestLayout({
      params: Promise.resolve({ contestId: CONTEST_ID }),
      children: null,
    } as unknown as Parameters<typeof ContestLayout>[0]),
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  loadContestResource.mockResolvedValue(null);
});

/**
 * The monitoring tab is offered exactly when the API says the viewer holds
 * contest.monitor on this contest (`may_monitor`); the layout restates no
 * permission rule of its own.
 */
describe("the monitoring tab", () => {
  test("is offered when the API says the viewer may monitor", async () => {
    loadContest.mockResolvedValue({ ...contest, mayMonitor: true });
    await renderLayout();

    expect(screen.getByRole("link", { name: "Monitoring" })).toHaveAttribute(
      "href",
      `/contests/${CONTEST_ID}/monitor`,
    );
  });

  test("is not offered otherwise", async () => {
    loadContest.mockResolvedValue({ ...contest, mayMonitor: false });
    await renderLayout();

    expect(screen.queryByRole("link", { name: "Monitoring" })).not.toBeInTheDocument();
    // The rest of the navigation is still there.
    expect(screen.getByRole("link", { name: "Leaderboard" })).toBeInTheDocument();
  });

  test("reads no staff list to decide it", async () => {
    loadContest.mockResolvedValue({ ...contest, mayMonitor: true });
    await renderLayout();

    expect(loadContestResource).not.toHaveBeenCalledWith(CONTEST_ID, "/managers", expect.anything());
  });
});
