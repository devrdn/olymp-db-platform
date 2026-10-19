import { render, screen } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// A Server Component: the API and the request are faked. Only the screens
// without a workspace are rendered here; the running screen's header is
// tested in play-header.test.tsx.
const { serverRequest, branding } = vi.hoisted(() => ({
  serverRequest: vi.fn(),
  branding: vi.fn(),
}));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("@/lib/api/branding", () => ({ branding }));
vi.mock("next/navigation", () => ({
  redirect: vi.fn(() => {
    throw new Error("NEXT_REDIRECT");
  }),
  useRouter: () => ({ refresh: vi.fn(), push: vi.fn() }),
}));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});
// The header's channel is its own hook's business.
vi.mock("./use-contest-events", () => ({
  useContestEvents: (_id: string, phase: "waiting" | "running") => ({
    offsetRef: { current: 0 },
    deadlineRef: { current: null },
    phase,
    channelError: null,
    resync: () => {},
    reopened: false,
  }),
}));

import PlayPage from "./page";

const CONTEST = "6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d01";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

function contestIs(status: string) {
  serverRequest.mockImplementation(async (path: string) => {
    if (path.startsWith("/contests?")) {
      return {
        items: [
          {
            id: CONTEST,
            status,
            enrollment: "open",
            question_mode: "multi",
            lang: "en",
            title: "The Greenhouse",
            starts_at: "2026-11-01T07:00:00Z",
            enrolled: true,
            scoring: "points",
            icpc_penalty_min: 20,
          },
        ],
        total: 1,
      };
    }
    throw new Error(`unexpected ${path}`);
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  branding.mockResolvedValue({ name: "USM", contact: "", feedback: "https://forms.gle/beta", images: {} });
});

async function renderPage() {
  render(
    await PlayPage({ params: Promise.resolve({ contestId: CONTEST }) } as unknown as Parameters<typeof PlayPage>[0]),
  );
}

const feedbackLink = () => screen.queryByRole("link", { name: new RegExp(dict.participant.feedback.link) });

describe("the play page's feedback link", () => {
  test("is offered in the waiting room, from the installation's setting", async () => {
    contestIs("published");

    await renderPage();

    expect(screen.getByText(dict.participant.play.waiting.body)).toBeInTheDocument();
    expect(feedbackLink()).toHaveAttribute("href", "https://forms.gle/beta");
  });

  test.each(["finished", "archived"])("is offered once the contest is %s", async (status) => {
    contestIs(status);

    await renderPage();

    expect(feedbackLink()).toHaveAttribute("href", "https://forms.gle/beta");
  });

  test("is not offered for a contest taken back to draft, which is not over", async () => {
    contestIs("draft");

    await renderPage();

    expect(screen.getByText(dict.participant.play.unavailable.body)).toBeInTheDocument();
    expect(feedbackLink()).not.toBeInTheDocument();
  });
});
