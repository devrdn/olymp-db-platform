import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, test, vi } from "vitest";

import type { FeedDetail, FeedItem } from "@/lib/api/monitor";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { FEED_LIMIT, type FeedState } from "./feed-list";
import { FEED_ROW_REM, LiveFeed } from "./live-feed";
import { feedItem } from "./test-fixtures";

const CONTEST = "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";
const ROW_PX = FEED_ROW_REM * 16;
const VIEW_PX = ROW_PX * 10;

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

function state(items: FeedItem[], overrides: Partial<FeedState> = {}): FeedState {
  return { items, newest: items.at(-1)?.cursor, olderAvailable: false, detached: false, missed: 0, gap: 0, ...overrides };
}

function many(from: number, to: number): FeedItem[] {
  const out: FeedItem[] = [];
  for (let i = from; i <= to; i += 1) out.push(feedItem(`c${i}`, { detail: { type: "query", id: i, sql: `SELECT ${i}`, sqlTruncated: false, status: "ok", durationMs: 1, rowCount: 1 } }));
  return out;
}

function withDetail(cursor: string, kind: string, detail: FeedDetail): FeedItem {
  return feedItem(cursor, { kind, detail, fullName: "Ivan Ivanov" });
}

type Props = Parameters<typeof LiveFeed>[0];

function renderFeed(feed: FeedState, overrides: Partial<Props> = {}) {
  const props: Props = {
    contestId: CONTEST,
    feed,
    kinds: [],
    onKinds: vi.fn(),
    onLoadOlder: vi.fn(),
    loadingOlder: false,
    onToLatest: vi.fn(),
    dict,
    locale: "en",
    ...overrides,
  };
  const view = render(<LiveFeed {...props} />);
  return { ...view, props, rerenderWith: (next: Partial<Props>) => view.rerender(<LiveFeed {...props} {...next} />) };
}

/** jsdom lays nothing out: the scroll box is given a height, and its offset is the test's to set. */
function scroller(): HTMLElement {
  const element = screen.getByTestId("feed-scroller");
  Object.defineProperty(element, "clientHeight", { value: VIEW_PX, configurable: true });
  return element;
}

function scrollTo(element: HTMLElement, top: number) {
  element.scrollTop = top;
  fireEvent.scroll(element);
}

describe("what each line says", () => {
  test("describes every kind in words", () => {
    const d = dict.workspace.monitor.feed.describe;
    renderFeed(
      state([
        withDetail("a", "query", { type: "query", id: 1, sql: "SELECT *\nFROM guests", sqlTruncated: false, status: "error", error: "no such table", durationMs: 4, rowCount: null }),
        withDetail("b", "answer", { type: "answer", questionOrd: 2, attemptNo: 3, value: "42", correct: true, points: 5 }),
        withDetail("c", "page_left", { type: "page_left", awayMs: 95_000 }),
        withDetail("d", "paste", { type: "paste", target: "editor", chars: 812, text: "SELECT", count: 1 }),
        withDetail("e", "ip_changed", { type: "ip_changed", from: "10.0.0.1", to: "10.0.0.2" }),
        withDetail("f", "parallel_session", { type: "parallel_session", otherIp: "10.0.0.9", userAgent: "Firefox" }),
        withDetail("g", "sign_in", { type: "audit", ip: "10.0.0.1" }),
        withDetail("h", "tab_created", { type: "tab", title: "Query 1" }),
      ]),
    );
    const list = screen.getByRole("list", { name: dict.workspace.monitor.feed.heading });

    expect(within(list).getByText("SELECT *")).toBeInTheDocument();
    expect(within(list).getByText(dict.workspace.monitor.feed.queryStatus.error)).toBeInTheDocument();
    expect(within(list).getByText(d.answerCorrect.replace("{n}", "2").replace("{attempt}", "3"))).toBeInTheDocument();
    expect(within(list).getByText(d.pageLeft.replace("{duration}", "1:35"))).toBeInTheDocument();
    expect(
      within(list).getByText(d.paste.replace("{chars}", "812").replace("{target}", d.pasteTarget.editor)),
    ).toBeInTheDocument();
    expect(within(list).getByText(d.ipChanged.replace("{from}", "10.0.0.1").replace("{to}", "10.0.0.2"))).toBeInTheDocument();
    expect(within(list).getByText(d.parallelSession.replace("{ip}", "10.0.0.9"))).toBeInTheDocument();
    expect(within(list).getByText(d.signInFrom.replace("{ip}", "10.0.0.1"))).toBeInTheDocument();
    expect(within(list).getAllByRole("link", { name: "Ivan Ivanov" })[0]).toHaveAttribute(
      "href",
      `/contests/${CONTEST}/monitor/a`,
    );
  });

  /** Identical pastes in a row arrive folded into one line; the line says how many they were. */
  test("says how many times a folded paste was repeated", () => {
    const d = dict.workspace.monitor.feed.describe;
    renderFeed(state([withDetail("a", "paste", { type: "paste", target: "notes", chars: 3, text: "abc", count: 4 })]));
    const list = screen.getByRole("list", { name: dict.workspace.monitor.feed.heading });

    expect(
      within(list).getByText(
        d.pasteRepeated.replace("{chars}", "3").replace("{target}", d.pasteTarget.notes).replace("{count}", "4"),
      ),
    ).toBeInTheDocument();
  });

  /** The server creates every participant's first SQL tab; that line is noise beside the rest. */
  test("de-emphasises tab events", () => {
    renderFeed(state([withDetail("h", "tab_created", { type: "tab", title: "Query 1" })]));
    const line = screen.getByText(dict.workspace.monitor.feed.describe.tabCreated.replace("{title}", "Query 1"));

    expect(line.closest("li")).toHaveAttribute("data-quiet", "true");
  });

  test("says a running query is running", () => {
    renderFeed(
      state([withDetail("a", "query", { type: "query", id: 1, sql: "SELECT pg_sleep(3)", sqlTruncated: false, status: "running", durationMs: null, rowCount: null })]),
    );
    expect(screen.getByText(dict.workspace.monitor.feed.queryStatus.running)).toBeInTheDocument();
  });
});

describe("the kind filters", () => {
  test("narrow to one group, add another, and go back to everything", async () => {
    const onKinds = vi.fn();
    const g = dict.workspace.monitor.feed.groups;
    const { rerenderWith } = renderFeed(state([]), { onKinds });

    await userEvent.click(screen.getByRole("button", { name: g.queries }));
    expect(onKinds).toHaveBeenLastCalledWith(["query"]);

    rerenderWith({ kinds: ["query"] });
    expect(screen.getByRole("button", { name: g.queries })).toHaveAttribute("aria-pressed", "true");
    await userEvent.click(screen.getByRole("button", { name: g.network }));
    expect(onKinds).toHaveBeenLastCalledWith(["query", "ip_changed", "parallel_session"]);

    await userEvent.click(screen.getByRole("button", { name: dict.workspace.monitor.feed.all }));
    expect(onKinds).toHaveBeenLastCalledWith([]);
  });
});

describe("following the newest item", () => {
  test("stays at the newest item while the organiser is there", () => {
    const { rerenderWith } = renderFeed(state(many(1, 20)));
    const box = scroller();
    scrollTo(box, 20 * ROW_PX - VIEW_PX);

    rerenderWith({ feed: state(many(1, 23)) });

    expect(box.scrollTop).toBe(23 * ROW_PX - VIEW_PX);
    expect(screen.queryByRole("button", { name: /new/ })).not.toBeInTheDocument();
  });

  test("stops following when scrolled up, and counts what arrived", async () => {
    const { rerenderWith } = renderFeed(state(many(1, 20)));
    const box = scroller();
    scrollTo(box, 20 * ROW_PX - VIEW_PX);
    scrollTo(box, 2 * ROW_PX);

    rerenderWith({ feed: state(many(1, 23)) });

    // The organiser's place is kept.
    expect(box.scrollTop).toBe(2 * ROW_PX);
    const jump = screen.getByRole("button", { name: dict.workspace.monitor.feed.newItems.replace("{n}", "3") });

    await userEvent.click(jump);
    expect(box.scrollTop).toBe(23 * ROW_PX - VIEW_PX);
    expect(screen.queryByRole("button", { name: /new/ })).not.toBeInTheDocument();
  });

  test("keeps the organiser's place when old items fall off the top", () => {
    const { rerenderWith } = renderFeed(state(many(1, FEED_LIMIT)));
    const box = scroller();
    scrollTo(box, 500 * ROW_PX);

    // Five new, five of the oldest dropped: the line that was at the top of
    // the view is five rows higher in the list now.
    rerenderWith({ feed: state(many(6, FEED_LIMIT + 5)) });
    expect(box.scrollTop).toBe(495 * ROW_PX);
  });

  test("keeps the organiser's place when older items arrive above", () => {
    const { rerenderWith } = renderFeed(state(many(101, 120), { olderAvailable: true }));
    const box = scroller();
    scrollTo(box, 0);

    rerenderWith({ feed: state(many(1, 120)) });
    expect(box.scrollTop).toBe(100 * ROW_PX);
  });

  test("after reading far back, the way to the latest reads it afresh and says what arrived", async () => {
    const onToLatest = vi.fn();
    renderFeed(state(many(1, 20), { detached: true, missed: 7 }), { onToLatest });
    const t = dict.workspace.monitor.feed;

    const back = screen.getByRole("button", { name: new RegExp(t.toLatest) });
    expect(back).toHaveTextContent(t.newItems.replace("{n}", "7"));
    await userEvent.click(back);
    expect(onToLatest).toHaveBeenCalled();
  });

  /**
   * The newest end was dropped to make room for older lines. Nothing new
   * arrived, and the organiser is at the bottom of what is held — the way
   * back to the dropped lines must still be there, and must not claim news.
   */
  test("detached, nothing missed, scrolled to the bottom: the way back is still offered", () => {
    renderFeed(state(many(1, 20), { detached: true, missed: 0 }));
    const box = scroller();
    scrollTo(box, 20 * ROW_PX - VIEW_PX);
    const t = dict.workspace.monitor.feed;

    expect(screen.getByRole("button", { name: t.toLatest })).toBeInTheDocument();
    expect(screen.queryByText(/new/)).not.toBeInTheDocument();
  });

  test("counts only what arrived, not what was dropped to load older lines", () => {
    const { rerenderWith } = renderFeed(state(many(101, 120), { olderAvailable: true }));
    const box = scroller();
    scrollTo(box, 0);

    // Older lines came in and the newest end, c120 among it, was dropped.
    rerenderWith({ feed: state(many(1, 110), { detached: true }) });

    expect(screen.queryByText(/new/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: dict.workspace.monitor.feed.toLatest })).toBeInTheDocument();
  });

  test("says how much was skipped after a long absence", () => {
    renderFeed(state(many(1, 3), { gap: 1200 }));
    expect(screen.getByText(dict.workspace.monitor.feed.gap.replace("{n}", "1200"))).toBeInTheDocument();
  });
});

describe("older items", () => {
  test("are loaded on request while there are any", async () => {
    const onLoadOlder = vi.fn();
    const { rerenderWith } = renderFeed(state(many(1, 3), { olderAvailable: true }), { onLoadOlder });

    await userEvent.click(screen.getByRole("button", { name: dict.workspace.monitor.feed.loadOlder }));
    expect(onLoadOlder).toHaveBeenCalled();

    rerenderWith({ feed: state(many(1, 3), { olderAvailable: false }) });
    expect(screen.queryByRole("button", { name: dict.workspace.monitor.feed.loadOlder })).not.toBeInTheDocument();
    expect(screen.getByText(dict.workspace.monitor.feed.start)).toBeInTheDocument();
  });
});

/**
 * A thousand lines in the DOM, each re-laid out on every poll, is what the
 * bound on the list exists to avoid; only the lines in view are rendered, and
 * the list still says how long it is.
 */
test("renders only the lines in view of a long feed", () => {
  const { rerenderWith } = renderFeed(state(many(1, FEED_LIMIT)));
  const box = scroller();
  act(() => scrollTo(box, 500 * ROW_PX));
  rerenderWith({ feed: state(many(1, FEED_LIMIT)) });

  const lines = within(screen.getByRole("list", { name: dict.workspace.monitor.feed.heading })).getAllByRole("listitem");
  expect(lines.length).toBeLessThan(40);
  expect(lines[0]).toHaveAttribute("aria-setsize", String(FEED_LIMIT));
  expect(screen.getByText("SELECT 501")).toBeInTheDocument();
});

/** On one participant's page every line is theirs: the name would only repeat the heading. */
describe("on one participant's page", () => {
  test("names nobody", () => {
    renderFeed(state([withDetail("a", "page_left", { type: "page_left", awayMs: 5_000 })]), { showParticipant: false });

    expect(screen.queryByRole("link", { name: "Ivan Ivanov" })).not.toBeInTheDocument();
    expect(screen.getByText(/Away from the page/)).toBeInTheDocument();
  });

  test("names whoever it was on the contest's feed", () => {
    renderFeed(state([withDetail("a", "page_left", { type: "page_left", awayMs: 5_000 })]));

    expect(screen.getByRole("link", { name: "Ivan Ivanov" })).toBeInTheDocument();
  });
});
