import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import type { AnswerInput, ClarificationQueue, QueueState, StaffClarification } from "@/lib/api/clarifications";
import { ANNOUNCEMENT_MAX_CHARS, ANSWER_MAX_CHARS } from "@/lib/api/clarifications";
import en from "@/lib/i18n/dictionaries/en";

import { ClarificationsDesk, DESK_POLL_MS } from "./clarifications-desk";

const t = en.workspace.clarifications;

const api = vi.hoisted(() => ({
  queues: {} as Record<string, ClarificationQueue | { refused: string }>,
  pages: {} as Record<string, (after: string | undefined, limit: number) => ClarificationQueue>,
  reads: [] as { state: QueueState; limit: number; after?: string }[],
  answers: [] as { id: string; answer: AnswerInput; mode: string }[],
  answerResult: null as null | { kind: "ok"; clarification: StaffClarification } | { kind: "refused"; code: string },
  posts: [] as string[],
  postResult: null as null | { kind: "refused"; code: string },
}));

vi.mock("@/lib/api/clarifications", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/clarifications")>()),
  fetchQueue: async (_contestId: string, state: QueueState, page: { limit: number; after?: string }) => {
    api.reads.push({ state, limit: page.limit, after: page.after });
    if (api.pages[state]) return { kind: "ok", queue: api.pages[state](page.after, page.limit) };
    const queue = api.queues[state];
    if (queue && "refused" in queue) return { kind: "refused", code: queue.refused };
    return { kind: "ok", queue: queue ?? { items: [], counts: { open: 0, answered: 0 } } };
  },
  answerClarification: async (_contestId: string, id: string, answer: AnswerInput, mode: string) => {
    api.answers.push({ id, answer, mode });
    return (
      api.answerResult ?? {
        kind: "ok",
        clarification: { ...waiting(id), answer: { ...answer, answeredAt: "2026-11-02T09:10:00.000000Z", answeredBy: "olga", editedAt: null } },
      }
    );
  },
  fetchAnnouncements: async () => ({ kind: "ok", list: { items: [], total: 0 } }),
  postAnnouncement: async (_contestId: string, body: string) => {
    api.posts.push(body);
    return api.postResult ?? { kind: "ok", announcement: { id: `a${api.posts.length}`, body, createdAt: "2026-11-02T09:20:00.000000Z" } };
  },
}));

const router = vi.hoisted(() => ({ refresh: vi.fn() }));
vi.mock("next/navigation", () => ({ useRouter: () => router }));

vi.mock("@/components/product/story-text", () => ({
  StoryText: ({ markdown }: { markdown: string }) => <div data-testid="markdown">{markdown}</div>,
}));

function waiting(id: string, overrides: Partial<StaffClarification> = {}): StaffClarification {
  return {
    id,
    questionId: null,
    body: "Is the year inclusive?",
    createdAt: "2026-11-02T09:00:00.000000Z",
    asker: { login: "ana", fullName: "Ana Ionescu" },
    answer: null,
    ...overrides,
  };
}

function answered(id: string, visibility: "private" | "public" = "private"): StaffClarification {
  return waiting(id, {
    answer: { body: "Yes.", declined: false, visibility, answeredAt: "2026-11-02T09:05:00.000000Z", answeredBy: "olga", editedAt: null },
  });
}

function queue(items: StaffClarification[], counts = { open: items.length, answered: 0 }): ClarificationQueue {
  return { items, counts };
}

function show(props: Partial<React.ComponentProps<typeof ClarificationsDesk>> = {}) {
  return render(
    <ClarificationsDesk
      contestId="11111111-1111-1111-1111-111111111111"
      running
      questions={[{ id: "q1", label: "Question 1: The greenhouse" }]}
      initialQueue={queue([waiting("c1", { questionId: "q1" })], { open: 1, answered: 2 })}
      initialAnnouncements={{ items: [], total: 0 }}
      dict={en}
      locale="en"
      {...props}
    />,
  );
}

beforeEach(() => {
  router.refresh.mockReset();
  api.queues = {};
  api.pages = {};
  api.reads = [];
  api.answers = [];
  api.answerResult = null;
  api.posts = [];
  api.postResult = null;
});

afterEach(() => {
  vi.useRealTimers();
});

describe("the staff desk", () => {
  test("shows the waiting questions with who asked and what about, and both counts", () => {
    show();

    expect(screen.getByRole("button", { name: t.waiting.replace("{n}", "1") })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: t.answered.replace("{n}", "2") })).toHaveAttribute("aria-pressed", "false");
    const item = screen.getByRole("article");
    expect(within(item).getByText(t.asker.replace("{name}", "Ana Ionescu").replace("{login}", "ana"))).toBeInTheDocument();
    expect(within(item).getByText("Question 1: The greenhouse")).toBeInTheDocument();
    expect(within(item).getByText("Is the year inclusive?")).toHaveClass("whitespace-pre-wrap");
  });

  test("says when nothing waits", () => {
    show({ initialQueue: queue([]) });
    expect(screen.getByText(t.emptyOpen)).toBeInTheDocument();
  });

  test("shows a participant's text as text, never as markup", () => {
    show({ initialQueue: queue([waiting("c1", { body: "<img src=x onerror=alert(1)>" })]) });
    const body = screen.getByText("<img src=x onerror=alert(1)>");
    expect(body.querySelector("img")).toBeNull();
  });

  // jsdom measures nothing, so this holds the rules that keep a long, unbroken
  // question inside a narrow column.
  test("keeps a very long question inside its column", () => {
    const long = "x".repeat(2000);
    show({ initialQueue: queue([waiting("c1", { body: long })]) });
    expect(screen.getByText(long)).toHaveClass("break-words");
    expect(screen.getByRole("article")).toHaveClass("min-w-0");
  });

  test("answers privately by default, and publicly when chosen, warning that the question goes public too", async () => {
    show();
    const item = screen.getByRole("article");
    const field = within(item).getByRole("textbox", { name: t.answerLabel });
    expect(field).toHaveAttribute("maxLength", String(ANSWER_MAX_CHARS));
    expect(within(item).getByRole("radio", { name: t.private })).toBeChecked();
    expect(within(item).getByText(t.publicNote)).toBeInTheDocument();

    await userEvent.type(field, "Yes, inclusive.");
    expect(within(item).getByText(t.counter.replace("{n}", "15").replace("{max}", String(ANSWER_MAX_CHARS)))).toBeInTheDocument();
    await userEvent.click(within(item).getByRole("radio", { name: t.public }));
    await userEvent.click(within(item).getByRole("button", { name: t.send }));

    await waitFor(() =>
      expect(api.answers).toEqual([
        { id: "c1", answer: { body: "Yes, inclusive.", declined: false, visibility: "public" }, mode: "answer" },
      ]),
    );
    expect(await screen.findByText(t.sent)).toBeInTheDocument();
    // It leaves the waiting list, and the counts follow.
    expect(screen.queryByRole("article")).toBeNull();
    expect(screen.getByRole("button", { name: t.answered.replace("{n}", "3") })).toBeInTheDocument();
  });

  test("declines with an empty reply", async () => {
    show();
    const item = screen.getByRole("article");
    const send = within(item).getByRole("button", { name: t.send });
    expect(send).toBeDisabled();
    await userEvent.click(within(item).getByRole("checkbox", { name: t.decline }));
    expect(send).toBeEnabled();
    await userEvent.click(send);
    await waitFor(() => expect(api.answers[0].answer).toEqual({ body: "", declined: true, visibility: "private" }));
  });

  test("shows a refusal in words and keeps the answer typed", async () => {
    api.answerResult = { kind: "refused", code: "clarification_already_answered" };
    show();
    const item = screen.getByRole("article");
    await userEvent.type(within(item).getByRole("textbox", { name: t.answerLabel }), "Yes.");
    await userEvent.click(within(item).getByRole("button", { name: t.send }));

    expect(await within(item).findByRole("alert")).toHaveTextContent(en.errors.clarification_already_answered);
    expect(within(item).getByRole("textbox", { name: t.answerLabel })).toHaveValue("Yes.");
  });

  test("edits an answer with the form filled from it", async () => {
    api.queues.answered = queue([answered("c9", "private")], { open: 1, answered: 1 });
    show();
    await userEvent.click(screen.getByRole("button", { name: t.answered.replace("{n}", "2") }));

    const item = await screen.findByRole("article");
    expect(within(item).getByTestId("markdown")).toHaveTextContent("Yes.");
    expect(within(item).getByText(t.visibilityTag.private)).toBeInTheDocument();
    await userEvent.click(within(item).getByRole("button", { name: t.edit }));
    const field = within(item).getByRole("textbox", { name: t.answerLabel });
    expect(field).toHaveValue("Yes.");
    await userEvent.clear(field);
    await userEvent.type(field, "Yes, until midnight.");
    await userEvent.click(within(item).getByRole("radio", { name: t.public }));
    await userEvent.click(within(item).getByRole("button", { name: t.save }));

    await waitFor(() =>
      expect(api.answers).toEqual([
        { id: "c9", answer: { body: "Yes, until midnight.", declined: false, visibility: "public" }, mode: "edit" },
      ]),
    );
  });

  test("polls every five seconds while visible, and keeps what is being typed", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    api.queues.open = queue([waiting("c1"), waiting("c2", { body: "A new one" })], { open: 2, answered: 2 });
    show();
    await userEvent.type(screen.getByRole("textbox", { name: t.answerLabel }), "Half an answer");

    await act(async () => {
      await vi.advanceTimersByTimeAsync(DESK_POLL_MS);
    });

    expect(api.reads.at(-1)?.state).toBe("open");
    expect(await screen.findByText("A new one")).toBeInTheDocument();
    expect(screen.getAllByRole("textbox", { name: t.answerLabel })[0]).toHaveValue("Half an answer");
    expect(screen.getByRole("button", { name: t.waiting.replace("{n}", "2") })).toBeInTheDocument();
  });

  // The navigation's waiting count is drawn by the server layout, once; the
  // desk is what notices it changed.
  test("redraws the navigation when the number waiting changes, and only then", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    api.queues.open = queue([waiting("c1", { questionId: "q1" })], { open: 1, answered: 2 });
    show();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(DESK_POLL_MS);
    });
    await waitFor(() => expect(api.reads).toHaveLength(1));
    expect(router.refresh).not.toHaveBeenCalled();

    await userEvent.type(screen.getByRole("textbox", { name: t.answerLabel }), "Yes.");
    await userEvent.click(screen.getByRole("button", { name: t.send }));
    await screen.findByText(t.sent);
    expect(router.refresh).toHaveBeenCalledTimes(1);

    api.queues.open = queue([waiting("c2", { body: "A new one" })], { open: 1, answered: 3 });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(DESK_POLL_MS);
    });
    await screen.findByText("A new one");
    expect(router.refresh).toHaveBeenCalledTimes(2);
  });

  test("reads nothing while the tab is hidden", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    show();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(DESK_POLL_MS * 3);
    });
    expect(api.reads).toHaveLength(0);
    visibility.mockRestore();
  });

  test("says when it may not read the queue, and stops", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    api.queues.open = { refused: "forbidden" };
    show();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(DESK_POLL_MS);
    });
    expect(await screen.findByText(t.forbidden)).toBeInTheDocument();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(DESK_POLL_MS * 2);
    });
    expect(api.reads).toHaveLength(1);
  });

  test("is read-only outside a running contest, and does not poll", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    show({ running: false });
    expect(screen.getByText(t.closed)).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: t.answerLabel })).toBeNull();
    expect(screen.queryByRole("textbox", { name: t.announcementLabel })).toBeNull();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(DESK_POLL_MS * 2);
    });
    expect(api.reads).toHaveLength(0);
  });

  test("posts an announcement, bounded, and lists it first", async () => {
    show({ initialAnnouncements: { items: [{ id: "a0", body: "Welcome.", createdAt: "2026-11-02T08:00:00.000000Z" }], total: 1 } });
    const field = screen.getByRole("textbox", { name: t.announcementLabel });
    expect(field).toHaveAttribute("maxLength", String(ANNOUNCEMENT_MAX_CHARS));
    await userEvent.type(field, "Ten minutes left.");
    await userEvent.click(screen.getByRole("button", { name: t.post }));

    await waitFor(() => expect(api.posts).toEqual(["Ten minutes left."]));
    expect(await screen.findByText(t.posted)).toBeInTheDocument();
    const notes = within(screen.getByRole("region", { name: t.announcements })).getAllByTestId("markdown");
    expect(notes.map((n) => n.textContent)).toEqual(["Ten minutes left.", "Welcome."]);
    expect(field).toHaveValue("");
  });

  test("answers from the keyboard alone", async () => {
    show();
    const item = screen.getByRole("article");
    within(item).getByRole("textbox", { name: t.answerLabel }).focus();
    await userEvent.keyboard("Yes.");
    await userEvent.tab();
    expect(within(item).getByRole("radio", { name: t.private })).toHaveFocus();
    await userEvent.keyboard("{ArrowDown}");
    expect(within(item).getByRole("radio", { name: t.public })).toBeChecked();
    await userEvent.tab();
    expect(within(item).getByRole("checkbox", { name: t.decline })).toHaveFocus();
    await userEvent.tab();
    expect(within(item).getByRole("button", { name: t.send })).toHaveFocus();
    await userEvent.keyboard("{Enter}");
    await waitFor(() => expect(api.answers[0].answer.visibility).toBe("public"));
  });

  // Each page names the last item held, so answers given meanwhile, which
  // move the list, neither repeat an item nor skip one.
  test("reaches answered questions past the hundred it keeps live, a page at a time", async () => {
    const all = Array.from({ length: 160 }, (_, i) => answered(`a${i}`));
    api.pages.answered = (after, limit) => {
      const from = after === undefined ? 0 : all.findIndex((item) => item.id === after) + 1;
      return queue(all.slice(from, from + limit), { open: 1, answered: all.length });
    };
    show();
    await userEvent.click(screen.getByRole("button", { name: t.answered.replace("{n}", "2") }));
    await waitFor(() => expect(screen.getAllByRole("article")).toHaveLength(50));

    for (const expected of [100, 150, 160]) {
      await userEvent.click(screen.getByRole("button", { name: t.older }));
      await waitFor(() => expect(screen.getAllByRole("article")).toHaveLength(expected));
    }
    expect(screen.queryByRole("button", { name: t.older })).toBeNull();
    expect(api.reads.slice(-3).map((r) => [r.after, r.limit])).toEqual([
      [undefined, 100],
      ["a99", 50],
      ["a149", 50],
    ]);
  });

  describe("keyboard focus", () => {
    test("goes to the field when an answer opens for editing, and back to Edit on Cancel", async () => {
      api.queues.answered = queue([answered("c9")], { open: 1, answered: 1 });
      show();
      await userEvent.click(screen.getByRole("button", { name: t.answered.replace("{n}", "2") }));
      const item = await screen.findByRole("article");

      await userEvent.click(within(item).getByRole("button", { name: t.edit }));
      expect(within(item).getByRole("textbox", { name: t.answerLabel })).toHaveFocus();

      await userEvent.click(within(item).getByRole("button", { name: t.cancel }));
      expect(within(item).getByRole("button", { name: t.edit })).toHaveFocus();
    });

    test("returns to Edit once an edit is saved", async () => {
      api.queues.answered = queue([answered("c9")], { open: 1, answered: 1 });
      show();
      await userEvent.click(screen.getByRole("button", { name: t.answered.replace("{n}", "2") }));
      const item = await screen.findByRole("article");
      await userEvent.click(within(item).getByRole("button", { name: t.edit }));
      await userEvent.click(within(item).getByRole("button", { name: t.save }));

      await waitFor(() => expect(within(item).getByRole("button", { name: t.edit })).toHaveFocus());
    });

    test("goes to the status line when an answered question leaves the list", async () => {
      show();
      const item = screen.getByRole("article");
      await userEvent.type(within(item).getByRole("textbox", { name: t.answerLabel }), "Yes.");
      await userEvent.click(within(item).getByRole("button", { name: t.send }));

      const status = await screen.findByText(t.sent);
      await waitFor(() => expect(status).toHaveFocus());
    });
  });
});

