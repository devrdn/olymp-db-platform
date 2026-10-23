import { beforeAll, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// A Server Component: the loaders are faked and the view only records its props.
const { loadContest, loadContestResource, view } = vi.hoisted(() => ({
  loadContest: vi.fn(async (id: string) => ({ id })),
  loadContestResource: vi.fn(async () => null),
  view: vi.fn(() => null),
}));
vi.mock("../contest", () => ({ loadContest, loadContestResource }));
vi.mock("./monitor-view", () => ({ MonitorView: view }));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});

import MonitorPage from "./page";

const CONTEST = "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

async function renderedProps(): Promise<Record<string, unknown>> {
  const element = await MonitorPage({
    params: Promise.resolve({ contestId: CONTEST }),
    searchParams: Promise.resolve({}),
  } as PageProps<"/contests/[contestId]/monitor">);
  return element.props as Record<string, unknown>;
}

describe("the monitoring page", () => {
  /** Every prop of the client view is serialised into the page, so the dictionary goes as one section. */
  test("hands the screen only the dictionary section it reads", async () => {
    const props = await renderedProps();

    expect(Object.keys(props.dict as object)).toEqual(["workspace"]);
    expect((props.dict as Dictionary).workspace).toBe(dict.workspace);
  });

  test("starts from an empty table and feed when the first reads find nothing", async () => {
    const props = await renderedProps();

    expect(props.roster).toEqual({ generatedAt: "", truncated: false, rows: [] });
    expect(props.feed).toEqual({ items: [], more: false });
  });
});
