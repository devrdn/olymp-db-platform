import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import { NOTES_COUNTER_FROM, NOTES_MAX_CHARS } from "@/lib/api/workspace";
import en from "@/lib/i18n/dictionaries/en";

import { NotesPanel } from "./notes-panel";
import { draftStorageKey } from "./use-autosave";
import { pasteTargetOf } from "./use-signals";

const t = en.participant.play.workspace.notes;

type Call = { url: string; init: RequestInit };
let calls: Call[] = [];
let answer: () => Response;

function ok() {
  return new Response(JSON.stringify({ updated_at: "2026-09-17T10:05:00Z" }), {
    status: 200,
    headers: { "content-type": "application/json" },
  });
}

function refused(status: number, code: string) {
  return () =>
    new Response(JSON.stringify({ error: { code, message: "no" } }), {
      status,
      headers: { "content-type": "application/json" },
    });
}

function show(initial: { body: string; updatedAt: string | null } | null = { body: "the butler", updatedAt: "v0" }) {
  return render(<NotesPanel contestId="c1" initial={initial} dict={en} />);
}

async function wait(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

function field() {
  return screen.getByRole("textbox", { name: t.label });
}

function edit(value: string) {
  fireEvent.change(field(), { target: { value } });
}

/** What the status line under the field says to a sighted reader. */
function visibleStatus() {
  return screen.getByTestId("notes-status").textContent;
}

beforeEach(() => {
  vi.useFakeTimers();
  window.localStorage.clear();
  calls = [];
  answer = ok;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init: RequestInit) => {
      calls.push({ url, init });
      return answer();
    }),
  );
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("the notes panel", () => {
  test("shows the saved notes in a labelled field bounded at the server's limit", () => {
    show();

    expect(field()).toHaveValue("the butler");
    expect(field()).toHaveAttribute("maxLength", String(NOTES_MAX_CHARS));
    expect(visibleStatus()).toBe(t.status.saved);
  });

  test("saves what was typed to the API a moment later", async () => {
    show();

    edit("the gardener");
    expect(visibleStatus()).toBe(t.status.saving);
    await wait(1500);

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe("/api/v1/contests/c1/play/notes");
    expect(calls[0].init).toMatchObject({ method: "PUT", credentials: "same-origin" });
    expect(JSON.parse(String(calls[0].init.body))).toEqual({ body: "the gardener" });
    expect(visibleStatus()).toBe(t.status.saved);
  });

  test("sends what is unsaved as a keepalive request when the page is hidden for good", async () => {
    show();

    edit("the gardener");
    await act(async () => window.dispatchEvent(new Event("pagehide")));

    expect(calls).toHaveLength(1);
    expect(calls[0].init).toMatchObject({ method: "PUT", keepalive: true, credentials: "same-origin" });
  });

  test("says a failed save will be retried, to a screen reader too", async () => {
    answer = refused(503, "internal_error");
    show();

    edit("the gardener");
    await wait(1500);

    expect(visibleStatus()).toBe(t.status.retrying);
    expect(screen.getByRole("status")).toHaveTextContent(t.status.retrying);
  });

  test("does not announce the routine save cycle", async () => {
    show();
    const live = screen.getByRole("status");
    const before = live.textContent;

    edit("the gardener");
    expect(live.textContent).toBe(before);
    await wait(1500);
    expect(live.textContent).toBe(before);
  });

  // What the SQL editor beside these notes does once the contest is over:
  // the saving stops, the draft stays, and the field goes on taking text.
  // A participant writing down what they worked out has no reason to be
  // treated differently from one typing a query, and taking the field away
  // from under a hand that is mid-sentence is the one answer that loses
  // something.
  test("says the contest is over when a save is refused for it, and still takes what is typed", async () => {
    answer = refused(409, "contest_finished");
    show();

    edit("the gardener");
    await wait(1500);

    expect(visibleStatus()).toBe(t.status.closed);
    expect(screen.getByRole("status")).toHaveTextContent(t.status.closed);
    expect(field()).not.toHaveAttribute("readonly");
    expect(JSON.parse(window.localStorage.getItem(draftStorageKey("c1", "notes")) ?? "null")).toMatchObject({
      text: "the gardener",
    });

    calls = [];
    edit("the gardener, in the library");
    await wait(30_000);

    expect(calls).toHaveLength(0);
    expect(field()).toHaveValue("the gardener, in the library");
    expect(JSON.parse(window.localStorage.getItem(draftStorageKey("c1", "notes")) ?? "null")).toMatchObject({
      text: "the gardener, in the library",
    });
  });

  test("shows the server's reason when it refuses the text itself", async () => {
    answer = refused(400, "workspace_notes_too_long");
    show();

    edit("the gardener");
    await wait(1500);

    expect(visibleStatus()).toBe(en.errors.workspace_notes_too_long);
  });

  test("shows no counter well below the limit", () => {
    show({ body: "x".repeat(NOTES_COUNTER_FROM - 1), updatedAt: "v0" });

    expect(screen.queryByTestId("notes-counter")).not.toBeInTheDocument();
  });

  test("shows the counter from 18,000 characters", () => {
    show();

    edit("x".repeat(NOTES_COUNTER_FROM));

    // Plain digits, with no locale grouping: this is rendered on the server
    // as well, and Node and the browser do not always group alike.
    expect(screen.getByTestId("notes-counter")).toHaveTextContent("18000 of 20000 characters");
  });

  test("shows the counter for notes that arrive already long", () => {
    show({ body: "x".repeat(19_500), updatedAt: "v0" });

    expect(screen.getByTestId("notes-counter")).toHaveTextContent("19500 of 20000 characters");
  });

  test("hides the counter again once the notes are shortened", () => {
    show();

    edit("x".repeat(NOTES_COUNTER_FROM));
    edit("short");

    expect(screen.queryByTestId("notes-counter")).not.toBeInTheDocument();
  });

  // The field stops accepting text at the limit, and a field that silently
  // stops is exactly what a screen reader user cannot see.
  test("announces once when the notes reach the limit", () => {
    show();
    const live = screen.getByTestId("notes-limit");

    edit("x".repeat(NOTES_MAX_CHARS - 1));
    expect(live).toHaveTextContent("");

    edit("x".repeat(NOTES_MAX_CHARS));
    expect(live).toHaveTextContent(t.limitReached);
    expect(live).toHaveAttribute("aria-live", "polite");
  });

  test("takes the announcement back once there is room again", () => {
    show();

    edit("x".repeat(NOTES_MAX_CHARS));
    edit("x".repeat(NOTES_MAX_CHARS - 1));

    expect(screen.getByTestId("notes-limit")).toHaveTextContent("");
  });

  test("shows a draft that is newer than the server copy, and saves it", async () => {
    window.localStorage.setItem(
      draftStorageKey("c1", "notes"),
      JSON.stringify({ text: "typed before the reload", base: "v0" }),
    );
    show();
    await act(async () => {});

    expect(field()).toHaveValue("typed before the reload");
    expect(calls).toHaveLength(1);
  });

  test("says the notes could not be loaded, and offers no field to overwrite them from", () => {
    show(null);

    expect(screen.getByText(t.failed)).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  });
});

test("a paste into the notes is watched as one into the notes", () => {
  show();
  expect(pasteTargetOf(field())).toBe("notes");
});

// Design §8: the notes are not private, and the field says so.
test("say under the field that the organiser can see them", () => {
  show();
  expect(t.observed).toBe("The organiser can see your notes.");
  expect(screen.getByText(t.observed)).toBeInTheDocument();
});
