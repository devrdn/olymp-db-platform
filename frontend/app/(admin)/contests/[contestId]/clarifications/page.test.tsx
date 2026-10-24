import { render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

// A Server Component: the loaders are faked and the desk only records its props.
const { loadContest, loadContestResource, desk } = vi.hoisted(() => ({
  loadContest: vi.fn(async (id: string) => ({ id, status: "running", languages: [{ code: "ro", isDefault: true }] })),
  loadContestResource: vi.fn(async (_id: string, path: string, parse: (payload: unknown) => unknown) => {
    if (path === "/questions") {
      return parse({
        items: [
          {
            id: "q2",
            ord: 2,
            kind: "text",
            points: 1,
            penalty_pct: 0,
            is_visible: true,
            choice_ids: [],
            texts: { ro: { title: "", body_md: "b" } },
          },
          {
            id: "q1",
            ord: 1,
            kind: "text",
            points: 1,
            penalty_pct: 0,
            is_visible: true,
            choice_ids: [],
            texts: { ro: { title: "Sera", body_md: "b" }, en: { title: "The greenhouse", body_md: "b" } },
          },
        ],
      });
    }
    return null;
  }),
  desk: vi.fn<(props: Record<string, unknown>) => null>(() => null),
}));
vi.mock("../contest", () => ({ loadContest, loadContestResource }));
vi.mock("./clarifications-desk", () => ({ ClarificationsDesk: desk }));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});

import ClarificationsPage from "./page";

const CONTEST = "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";

async function show() {
  render(
    await ClarificationsPage({
      params: Promise.resolve({ contestId: CONTEST }),
      searchParams: Promise.resolve({}),
    } as PageProps<"/contests/[contestId]/clarifications">),
  );
  return desk.mock.calls.at(-1)![0];
}

describe("the participant questions page", () => {
  test("names the screen and says what participants see", async () => {
    await show();
    expect(screen.getByRole("heading", { name: en.workspace.tabs.clarifications })).toBeInTheDocument();
    expect(screen.getByText(en.workspace.clarifications.lede)).toBeInTheDocument();
  });

  test("names the questions in order, in the page's language where they have it", async () => {
    const props = await show();
    expect(props.questions).toEqual([
      { id: "q1", label: "About question 1: The greenhouse" },
      { id: "q2", label: "About question 2" },
    ]);
  });

  test("starts from empty lists when the first reads find nothing, and knows the contest runs", async () => {
    const props = await show();
    expect(props.running).toBe(true);
    expect(props.initialQueue).toEqual({ items: [], counts: { open: 0, answered: 0 } });
    expect(props.initialAnnouncements).toEqual({ items: [], total: 0 });
    expect(Object.keys(props.dict as object).sort()).toEqual(["errors", "workspace"]);
  });
});
