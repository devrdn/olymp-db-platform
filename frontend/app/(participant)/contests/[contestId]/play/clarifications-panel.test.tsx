import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import type { FeedItem } from "@/lib/api/clarifications";
import { MAX_OPEN_QUESTIONS, QUESTION_MAX_CHARS } from "@/lib/api/clarifications";
import type { OpenPlayQuestion } from "@/lib/api/play";
import en from "@/lib/i18n/dictionaries/en";

import { ClarificationSignalProvider, useClarificationPublisher, type ClarificationPublisher } from "./clarification-signal";
import { ClarificationsPanel } from "./clarifications-panel";
import type { QuestionEntry } from "./questions-panel";
import type { AskResult, ClarificationsResult } from "./requests";

const t = en.participant.play.clarifications;

const api = vi.hoisted(() => ({
  feeds: [] as ClarificationsResult[],
  fetches: [] as (string | undefined)[],
  ask: { kind: "refused", code: "unreachable" } as AskResult,
  asks: [] as { body: string; questionId: string | null }[],
}));

vi.mock("./requests", () => ({
  fetchClarifications: async (_contestId: string, before?: string) => {
    api.fetches.push(before);
    return api.feeds.length > 1 ? api.feeds.shift()! : api.feeds[0];
  },
  askOrganisers: async (_contestId: string, body: string, questionId: string | null) => {
    api.asks.push({ body, questionId });
    return api.ask;
  },
}));

// Organisers' Markdown is rendered in the browser on demand; the stand-in shows
// which texts took that path.
vi.mock("./clarification-text", () => ({
  ClarificationText: ({ markdown }: { markdown: string }) => <div data-testid="markdown">{markdown}</div>,
}));

function stamp(minute: number) {
  return `2026-11-02T09:${String(minute).padStart(2, "0")}:00.000000Z`;
}

function mine(overrides: Partial<Extract<FeedItem, { kind: "clarification" }>> = {}): FeedItem {
  return {
    kind: "clarification",
    id: "mine-1",
    postedAt: stamp(1),
    mine: true,
    questionId: null,
    body: "Is the year inclusive?",
    askedAt: stamp(1),
    answer: null,
    ...overrides,
  };
}

function answered(minute: number, overrides: Partial<Extract<FeedItem, { kind: "clarification" }>> = {}): FeedItem {
  return mine({
    id: `answered-${minute}`,
    postedAt: stamp(minute),
    answer: { body: "Yes, **inclusive**.", declined: false, visibility: "private", answeredAt: stamp(minute), editedAt: null },
    ...overrides,
  });
}

function announcement(minute: number, body = "Ten minutes left."): FeedItem {
  return { kind: "announcement", id: `note-${minute}`, postedAt: stamp(minute), body };
}

function feed(items: FeedItem[], extra: { next?: string | null; open?: number } = {}): ClarificationsResult {
  return { kind: "ok", feed: { items, next: extra.next ?? null, open: extra.open ?? 0 } };
}

function question(id: string, index: number, title = ""): QuestionEntry {
  const q: OpenPlayQuestion = {
    locked: false,
    id,
    kind: "text",
    points: 10,
    isBonus: false,
    choiceIds: [],
    title,
    bodyMd: "",
    choices: {},
    attemptsRemaining: undefined,
    closed: false,
    canAnswer: true,
    correct: false,
    pointsAwarded: 0,
  };
  return { question: q, index, body: null };
}

const captured: { current: ClarificationPublisher | null } = { current: null };
function Capture() {
  const publisher = useClarificationPublisher();
  useEffect(() => {
    captured.current = publisher;
  });
  return null;
}
const publisher: ClarificationPublisher = {
  hint: () => captured.current!.hint(),
  connected: () => captured.current!.connected(),
  finish: () => captured.current!.finish(),
};

function show(
  props: Partial<React.ComponentProps<typeof ClarificationsPanel>> = {},
  questionEntries: QuestionEntry[] = [question("q1", 1, "The greenhouse"), { question: { locked: true, position: 2 }, index: 2, body: null }],
) {
  const onUnreadChange = props.onUnreadChange ?? vi.fn();
  const view = render(
    <ClarificationSignalProvider>
      <Capture />
      <ClarificationsPanel
        contestId="11111111-1111-1111-1111-111111111111"
        accountId="u1"
        questionEntries={questionEntries}
        active
        onUnreadChange={onUnreadChange}
        dict={en}
        locale="en"
        {...props}
      />
    </ClarificationSignalProvider>,
  );
  return { ...view, onUnreadChange };
}

beforeEach(() => {
  api.feeds = [feed([])];
  api.fetches = [];
  api.asks = [];
  api.ask = { kind: "refused", code: "unreachable" };
  localStorage.clear();
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("the questions to the organisers", () => {
  test("says it is loading, then that there is nothing yet", async () => {
    show();
    expect(screen.getByText(t.loading)).toBeInTheDocument();
    expect(await screen.findByText(t.empty)).toBeInTheDocument();
    expect(api.fetches).toEqual([undefined]);
  });

  test("offers a retry when the first read fails, and reads again", async () => {
    api.feeds = [{ kind: "refused", code: "play_read_too_often" }, feed([announcement(2)])];
    show();
    expect(await screen.findByText(t.failed)).toBeInTheDocument();
    expect(screen.getByText(en.errors.play_read_too_often)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: t.retry }));

    expect(await screen.findByText("Ten minutes left.")).toBeInTheDocument();
  });

  test("shows announcements apart from the questions, and organisers' text as Markdown", async () => {
    api.feeds = [feed([announcement(5), answered(4), mine({ id: "mine-2", postedAt: stamp(3), askedAt: stamp(3) })])];
    show();

    const notes = await screen.findByRole("region", { name: t.announcements });
    expect(within(notes).getByTestId("markdown")).toHaveTextContent("Ten minutes left.");
    const thread = screen.getByRole("region", { name: t.thread });
    const items = within(thread).getAllByRole("article");
    expect(items).toHaveLength(2);
    expect(within(items[0]).getByTestId("markdown")).toHaveTextContent("Yes, **inclusive**.");
    expect(within(items[1]).getByText(t.waiting)).toBeInTheDocument();
  });

  test("shows a participant's text as text, never as markup", async () => {
    api.feeds = [feed([mine({ body: "<b>bold</b>\nsecond line" })])];
    show();

    const body = await screen.findByText(/<b>bold<\/b>/);
    expect(body.querySelector("b")).toBeNull();
    expect(body).toHaveClass("whitespace-pre-wrap");
  });

  test("marks another participant's published answer without naming them, and a declined question as declined", async () => {
    api.feeds = [
      feed([
        answered(6, {
          id: "public",
          mine: false,
          questionId: "q1",
          answer: { body: "", declined: true, visibility: "public", answeredAt: stamp(6), editedAt: stamp(7) },
        }),
      ]),
    ];
    show();

    const item = await screen.findByRole("article");
    expect(within(item).getByText(t.public)).toBeInTheDocument();
    expect(within(item).queryByText(t.mine)).toBeNull();
    expect(within(item).getByText(t.declined)).toBeInTheDocument();
    expect(within(item).getByText(t.edited)).toBeInTheDocument();
    expect(within(item).getByText(t.aboutQuestionTitled.replace("{n}", "1").replace("{title}", "The greenhouse"))).toBeInTheDocument();
  });

  test("says when the participant's own answer was published to everyone", async () => {
    api.feeds = [feed([answered(4, { answer: { body: "x", declined: false, visibility: "public", answeredAt: stamp(4), editedAt: null } })])];
    show();
    expect(await screen.findByText(t.published)).toBeInTheDocument();
  });

  test("keeps a very long question and answer inside the panel", async () => {
    const long = "x".repeat(QUESTION_MAX_CHARS);
    api.feeds = [feed([answered(4, { body: long })])];
    show();
    const body = await screen.findByText(long);
    expect(body).toHaveClass("break-words");
  });

  test("offers only the questions the participant can see, and the contest in general", async () => {
    show();
    await screen.findByText(t.empty);
    const picker = screen.getByRole("combobox", { name: t.about });
    const options = within(picker).getAllByRole("option").map((o) => o.textContent);
    expect(options).toEqual([t.general, t.questionOptionTitled.replace("{n}", "1").replace("{title}", "The greenhouse")]);
  });

  test("counts characters against the bound and sends the question with Ctrl+Enter", async () => {
    api.feeds = [feed([]), feed([mine()], { open: 1 })];
    api.ask = { kind: "ok", asked: mine() as Extract<FeedItem, { kind: "clarification" }> };
    show();
    await screen.findByText(t.empty);

    const field = screen.getByRole("textbox", { name: t.label });
    expect(field).toHaveAttribute("maxLength", String(QUESTION_MAX_CHARS));
    await userEvent.selectOptions(screen.getByRole("combobox", { name: t.about }), "q1");
    await userEvent.type(field, "Why?");
    expect(screen.getByText(t.counter.replace("{n}", "4").replace("{max}", String(QUESTION_MAX_CHARS)))).toBeInTheDocument();

    await userEvent.keyboard("{Control>}{Enter}{/Control}");

    await waitFor(() => expect(api.asks).toEqual([{ body: "Why?", questionId: "q1" }]));
    expect(await screen.findByText(t.sent)).toBeInTheDocument();
    expect(field).toHaveValue("");
    // The feed is read again, so the new question shows as the server keeps it.
    expect(await screen.findByText("Is the year inclusive?")).toBeInTheDocument();
  });

  test("is reachable from the keyboard alone: picker, field, send", async () => {
    api.ask = { kind: "ok", asked: mine() as Extract<FeedItem, { kind: "clarification" }> };
    show();
    await screen.findByText(t.empty);

    await userEvent.tab();
    expect(screen.getByRole("combobox", { name: t.about })).toHaveFocus();
    await userEvent.tab();
    expect(screen.getByRole("textbox", { name: t.label })).toHaveFocus();
    await userEvent.keyboard("Where is the key?");
    await userEvent.tab();
    expect(screen.getByRole("button", { name: t.send })).toHaveFocus();
    await userEvent.keyboard("{Enter}");
    await waitFor(() => expect(api.asks).toHaveLength(1));
  });

  test("shows a refusal in words and keeps the text", async () => {
    api.ask = { kind: "refused", code: "clarification_too_often" };
    show();
    await screen.findByText(t.empty);
    const field = screen.getByRole("textbox", { name: t.label });
    await userEvent.type(field, "Why?");
    await userEvent.click(screen.getByRole("button", { name: t.send }));

    expect(await screen.findByRole("alert")).toHaveTextContent(en.errors.clarification_too_often);
    expect(field).toHaveValue("Why?");
  });

  test("sends nothing while the field is empty", async () => {
    show();
    await screen.findByText(t.empty);
    expect(screen.getByRole("button", { name: t.send })).toBeDisabled();
  });

  test("stops asking at twenty questions waiting and says why", async () => {
    api.feeds = [feed([mine()], { open: MAX_OPEN_QUESTIONS })];
    show();
    expect(await screen.findByText(t.atCap)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t.send })).toBeDisabled();
  });

  test("turns read-only once the contest is over for the participant, keeping the answers", async () => {
    api.feeds = [feed([answered(4)])];
    show();
    await screen.findByRole("article");

    act(() => publisher.finish());

    expect(screen.getByText(t.closed)).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: t.label })).toBeNull();
    expect(screen.getByRole("article")).toBeInTheDocument();
  });

  test("turns read-only when asking is refused because the contest ended", async () => {
    api.ask = { kind: "refused", code: "contest_ended" };
    show();
    await screen.findByText(t.empty);
    await userEvent.type(screen.getByRole("textbox", { name: t.label }), "Too late?");
    await userEvent.click(screen.getByRole("button", { name: t.send }));
    expect(await screen.findByText(t.closed)).toBeInTheDocument();
  });

  test("reads again a moment after a hint, spread so a whole contest does not ask at once", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.spyOn(Math, "random").mockReturnValue(0.5);
    api.feeds = [feed([]), feed([announcement(9, "New announcement")])];
    show();
    await screen.findByText(t.empty);

    act(() => publisher.hint());
    expect(api.fetches).toHaveLength(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });

    expect(api.fetches).toHaveLength(2);
    expect(await screen.findByText("New announcement")).toBeInTheDocument();
  });

  test("reads again when the channel reconnects, unless it has just read", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    show();
    await screen.findByText(t.empty);

    act(() => publisher.connected());
    expect(api.fetches).toHaveLength(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    act(() => publisher.connected());
    await waitFor(() => expect(api.fetches).toHaveLength(2));
  });

  test("loads older entries with the cursor the page gave", async () => {
    api.feeds = [feed([announcement(9)], { next: "older-cursor" }), feed([announcement(1, "From the start")])];
    show();
    await userEvent.click(await screen.findByRole("button", { name: t.older }));

    expect(await screen.findByText("From the start")).toBeInTheDocument();
    expect(api.fetches).toEqual([undefined, "older-cursor"]);
    expect(screen.queryByRole("button", { name: t.older })).toBeNull();
  });
});

describe("keeping the list right", () => {
  test("a hint arriving while a read is pending does not push that read back", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.spyOn(Math, "random").mockReturnValue(1);
    show();
    await screen.findByText(t.empty);

    act(() => publisher.hint());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    act(() => publisher.hint());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_500);
    });

    // The first hint's read happened 3 s after it, not 3 s after the second.
    expect(api.fetches).toHaveLength(2);
  });

  test("an answer taken back from everyone leaves the older pages too", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.spyOn(Math, "random").mockReturnValue(0);
    const unpublished = answered(2, { id: "taken-back", mine: false, body: "Soon private again" });
    api.feeds = [
      feed([announcement(9)], { next: "older-1" }),
      feed([unpublished, announcement(1, "From the start")]),
      // The re-read after a hint: the newest page, then the older one again.
      feed([announcement(9)], { next: "older-1" }),
      feed([announcement(1, "From the start")]),
    ];
    show();
    await userEvent.click(await screen.findByRole("button", { name: t.older }));
    expect(await screen.findByText("Soon private again")).toBeInTheDocument();

    act(() => publisher.hint());
    await waitFor(() => expect(screen.queryByText("Soon private again")).toBeNull());
    expect(screen.getByText("From the start")).toBeInTheDocument();
    expect(api.fetches).toEqual([undefined, "older-1", undefined, "older-1"]);
  });

  test("says the answers stay only while the page is open, once the contest is over", async () => {
    show();
    await screen.findByText(t.empty);
    act(() => publisher.finish());
    expect(screen.getByText(t.closed)).toHaveTextContent(/until you leave this page/);
  });
});

describe("the unread marker", () => {
  test("reports news while the tab is hidden, and nothing for the participant's own waiting question", async () => {
    api.feeds = [feed([mine()])];
    const { onUnreadChange, rerender } = show({ active: false });
    await screen.findByRole("article");
    expect(onUnreadChange).toHaveBeenLastCalledWith(false);

    api.feeds = [feed([answered(8), mine()])];
    vi.spyOn(Math, "random").mockReturnValue(0);
    act(() => publisher.hint());
    await screen.findByText("Yes, **inclusive**.");
    await waitFor(() => expect(onUnreadChange).toHaveBeenLastCalledWith(true));

    rerender(
      <ClarificationSignalProvider>
        <ClarificationsPanel
          contestId="11111111-1111-1111-1111-111111111111"
          accountId="u1"
          questionEntries={[]}
          active
          onUnreadChange={onUnreadChange}
          dict={en}
          locale="en"
        />
      </ClarificationSignalProvider>,
    );
    await waitFor(() => expect(onUnreadChange).toHaveBeenLastCalledWith(false));
  });

  test("remembers what was read across a reload, per account", async () => {
    api.feeds = [feed([announcement(5)])];
    const first = show({ active: true });
    await screen.findByText("Ten minutes left.");
    first.unmount();

    const again = show({ active: false });
    await screen.findByText("Ten minutes left.");
    expect(again.onUnreadChange).toHaveBeenLastCalledWith(false);
    again.unmount();

    const someoneElse = show({ active: false, accountId: "u2" });
    await screen.findByText("Ten minutes left.");
    await waitFor(() => expect(someoneElse.onUnreadChange).toHaveBeenLastCalledWith(true));
  });
});
