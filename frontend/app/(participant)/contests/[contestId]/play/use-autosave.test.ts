import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";

import {
  attachEngine,
  AUTOSAVE_DRAFT_WRITE_MS,
  AutosaveEngine,
  draftStorageKey,
  purgeForeignDrafts,
  textFingerprint,
  useAutosave,
  type AutosaveOptions,
} from "./use-autosave";

type SaveFn = AutosaveOptions["save"];

/** A promise the test settles by hand, to hold a save in flight. */
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const KEY = draftStorageKey("u1", "c1", "notes");

function readDraft(): { text: string; base: string | null; sent?: string[] } | null {
  const raw = window.localStorage.getItem(KEY);
  return raw === null ? null : JSON.parse(raw);
}

let version = 0;
/** A save that succeeds at once and names a fresh version each time. */
const succeed = () => vi.fn<SaveFn>(async () => `v${++version}`);

function mount(overrides: Partial<AutosaveOptions> = {}) {
  const save = overrides.save ?? succeed();
  const onRestore = overrides.onRestore ?? vi.fn();
  let renders = 0;
  const hook = renderHook(
    (props: AutosaveOptions) => {
      renders++;
      return useAutosave(props);
    },
    {
      initialProps: {
        accountId: "u1",
        contestId: "c1",
        documentKey: "notes",
        initialText: "server",
        initialVersion: "v0",
        ...overrides,
        save,
        onRestore,
      },
    },
  );
  return { ...hook, save, onRestore, renders: () => renders };
}

/** Moves the fake clock and lets every promise the move released settle. */
async function wait(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

function type(hook: ReturnType<typeof mount>, text: string) {
  act(() => hook.result.current.setValue(text));
}

function setVisibility(state: DocumentVisibilityState) {
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => state });
}

beforeEach(() => {
  vi.useFakeTimers();
  window.localStorage.clear();
  version = 0;
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  setVisibility("visible");
});

describe("when a save leaves", () => {
  test("a second and a half after the last edit", async () => {
    const hook = mount();

    type(hook, "a");
    await wait(1000);
    type(hook, "ab");
    await wait(1499);
    expect(hook.save).not.toHaveBeenCalled();

    await wait(1);
    expect(hook.save).toHaveBeenCalledTimes(1);
    expect(hook.save).toHaveBeenCalledWith("ab", { keepalive: false });
  });

  test("at least every ten seconds under continuous typing", async () => {
    const hook = mount();

    let text = "";
    for (let elapsed = 0; elapsed < 10_000; elapsed += 1000) {
      text += "x";
      type(hook, text);
      await wait(1000);
    }

    expect(hook.save).toHaveBeenCalledTimes(1);
    expect(hook.save).toHaveBeenCalledWith("xxxxxxxxxx", { keepalive: false });
  });

  test("at once on flush", async () => {
    const hook = mount();

    type(hook, "a");
    await act(async () => hook.result.current.flush());

    expect(hook.save).toHaveBeenCalledWith("a", { keepalive: false });
    await wait(5000);
    expect(hook.save).toHaveBeenCalledTimes(1);
  });

  test("with keepalive the moment the tab is hidden", async () => {
    const hook = mount();

    type(hook, "a");
    setVisibility("hidden");
    await act(async () => document.dispatchEvent(new Event("visibilitychange")));

    expect(hook.save).toHaveBeenCalledWith("a", { keepalive: true });
  });

  test("not when the tab becomes visible again", async () => {
    const hook = mount();

    type(hook, "a");
    await act(async () => document.dispatchEvent(new Event("visibilitychange")));

    expect(hook.save).not.toHaveBeenCalled();
  });

  test("with keepalive on pagehide", async () => {
    const hook = mount();

    type(hook, "a");
    await act(async () => window.dispatchEvent(new Event("pagehide")));

    expect(hook.save).toHaveBeenCalledWith("a", { keepalive: true });
  });

  test("with keepalive when the editor goes away with unsaved text", async () => {
    const hook = mount();

    type(hook, "a");
    hook.unmount();

    expect(hook.save).toHaveBeenCalledWith("a", { keepalive: true });
  });
});

describe("what is not sent", () => {
  test("text equal to what the server already has", async () => {
    const hook = mount();

    type(hook, "serverX");
    type(hook, "server");
    await wait(15_000);
    await act(async () => window.dispatchEvent(new Event("pagehide")));

    expect(hook.save).not.toHaveBeenCalled();
    expect(hook.result.current.status).toEqual({ kind: "saved" });
  });

  test("text equal to the last save", async () => {
    const hook = mount();

    type(hook, "a");
    await wait(1500);
    type(hook, "ab");
    type(hook, "a");
    await wait(15_000);

    expect(hook.save).toHaveBeenCalledTimes(1);
  });

  test("a second request while one is in flight; the edits follow when it answers", async () => {
    const first = deferred<string>();
    const save = vi.fn<SaveFn>().mockReturnValueOnce(first.promise).mockResolvedValue("v2");
    const hook = mount({ save });

    type(hook, "a");
    await wait(1500);
    type(hook, "ab");
    await wait(15_000);
    expect(save).toHaveBeenCalledTimes(1);
    expect(hook.result.current.status).toEqual({ kind: "saving" });

    await act(async () => first.resolve("v1"));

    expect(save).toHaveBeenCalledTimes(2);
    expect(save).toHaveBeenLastCalledWith("ab", { keepalive: false });
  });

  test("a follow-up when nothing changed during the request", async () => {
    const first = deferred<string>();
    const save = vi.fn<SaveFn>().mockReturnValueOnce(first.promise);
    const hook = mount({ save });

    type(hook, "a");
    await wait(1500);
    await act(async () => first.resolve("v1"));
    await wait(15_000);

    expect(save).toHaveBeenCalledTimes(1);
    expect(hook.result.current.status).toEqual({ kind: "saved" });
  });
});

describe("a save that fails", () => {
  test("is retried after 2, 4, 8, 16 and then every 30 seconds", async () => {
    const save = vi.fn<SaveFn>().mockRejectedValue(new TypeError("Failed to fetch"));
    const hook = mount({ save });

    type(hook, "a");
    await wait(1500);
    expect(save).toHaveBeenCalledTimes(1);
    expect(hook.result.current.status).toEqual({ kind: "retrying" });

    const gaps = [2000, 4000, 8000, 16_000, 30_000, 30_000];
    for (const [index, gap] of gaps.entries()) {
      await wait(gap - 1);
      expect(save).toHaveBeenCalledTimes(index + 1);
      await wait(1);
      expect(save).toHaveBeenCalledTimes(index + 2);
    }
  });

  test("keeps its text in the draft while it waits", async () => {
    const save = vi.fn<SaveFn>().mockRejectedValue(new ApiError("internal_error", 500, "boom"));
    const hook = mount({ save });

    type(hook, "a");
    await wait(1500);

    expect(readDraft()?.text).toBe("a");
  });

  test("does not let a new edit cut the wait short", async () => {
    const save = vi.fn<SaveFn>().mockRejectedValueOnce(new ApiError("internal_error", 502, "boom")).mockResolvedValue("v1");
    const hook = mount({ save });

    type(hook, "a");
    await wait(1500);
    type(hook, "ab");
    await wait(1999);
    expect(save).toHaveBeenCalledTimes(1);

    await wait(1);
    expect(save).toHaveBeenLastCalledWith("ab", { keepalive: false });
    expect(hook.result.current.status).toEqual({ kind: "saved" });
  });

  test("starts the pause from two seconds again after a success", async () => {
    const save = vi
      .fn<SaveFn>()
      .mockRejectedValueOnce(new TypeError("offline"))
      .mockRejectedValueOnce(new TypeError("offline"))
      .mockResolvedValueOnce("v1")
      .mockRejectedValueOnce(new TypeError("offline"))
      .mockResolvedValue("v2");
    const hook = mount({ save });

    type(hook, "a");
    await wait(1500 + 2000 + 4000);
    expect(save).toHaveBeenCalledTimes(3);

    type(hook, "ab");
    await wait(1500);
    expect(save).toHaveBeenCalledTimes(4);
    await wait(2000);
    expect(save).toHaveBeenCalledTimes(5);
  });

  test("waits as long as a 429 said", async () => {
    const save = vi
      .fn<SaveFn>()
      .mockRejectedValueOnce(new ApiError("workspace_too_often", 429, "slow", undefined, undefined, undefined, 7))
      .mockResolvedValue("v1");
    const hook = mount({ save });

    type(hook, "a");
    await wait(1500);
    await wait(6999);
    expect(save).toHaveBeenCalledTimes(1);
    expect(hook.result.current.status).toEqual({ kind: "retrying" });

    await wait(1);
    expect(save).toHaveBeenCalledTimes(2);
  });

  // Refusals count against the budget, so a zero wait must not loop.
  test("waits at least the first pause when a 429 said to wait zero seconds", async () => {
    const save = vi
      .fn<SaveFn>()
      .mockRejectedValueOnce(new ApiError("workspace_too_often", 429, "slow", undefined, undefined, undefined, 0))
      .mockResolvedValue("v1");
    const hook = mount({ save });

    type(hook, "a");
    await wait(1500);
    await wait(1999);
    expect(save).toHaveBeenCalledTimes(1);

    await wait(1);
    expect(save).toHaveBeenCalledTimes(2);
  });

  test("falls back to the pause when a 429 named no wait", async () => {
    const save = vi
      .fn<SaveFn>()
      .mockRejectedValueOnce(new ApiError("workspace_too_often", 429, "slow"))
      .mockResolvedValue("v1");
    const hook = mount({ save });

    type(hook, "a");
    await wait(1500 + 2000);

    expect(save).toHaveBeenCalledTimes(2);
  });

  test("stops for good once the contest has finished, and keeps the draft", async () => {
    const save = vi.fn<SaveFn>().mockRejectedValue(new ApiError("contest_finished", 409, "over"));
    const hook = mount({ save });

    type(hook, "a");
    await wait(1500);
    expect(hook.result.current.status).toEqual({ kind: "closed", code: "contest_finished" });

    type(hook, "ab");
    await wait(60_000);
    await act(async () => hook.result.current.flush());
    await act(async () => window.dispatchEvent(new Event("pagehide")));

    expect(save).toHaveBeenCalledTimes(1);
    expect(readDraft()?.text).toBe("ab");
  });

  test("stops the same way when the contest has ended", async () => {
    const save = vi.fn<SaveFn>().mockRejectedValue(new ApiError("contest_ended", 409, "no"));
    const hook = mount({ save });

    type(hook, "a");
    await wait(1500);

    expect(hook.result.current.status).toEqual({ kind: "closed", code: "contest_ended" });
  });

  test("is not retried when the server refused that text, until the text changes", async () => {
    const save = vi
      .fn<SaveFn>()
      .mockRejectedValueOnce(new ApiError("workspace_notes_too_long", 400, "long"))
      .mockResolvedValue("v1");
    const hook = mount({ save });

    type(hook, "a");
    await wait(1500);
    expect(hook.result.current.status).toEqual({ kind: "rejected", code: "workspace_notes_too_long" });
    await wait(60_000);
    await act(async () => hook.result.current.flush());
    expect(save).toHaveBeenCalledTimes(1);

    type(hook, "b");
    await wait(1500);
    expect(save).toHaveBeenCalledTimes(2);
    expect(hook.result.current.status).toEqual({ kind: "saved" });
  });
});

describe("two saves that overlap", () => {
  /**
   * Hiding the page sends the newest text while an ordinary save runs, so two
   * requests overlap and neither answer proves what the database committed
   * last. The engine confirms neither and saves once more after both answer.
   */
  test("confirm nothing on their own, and are followed by one more save", async () => {
    const first = deferred<string>();
    const second = deferred<string>();
    const save = vi.fn<SaveFn>().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise).mockResolvedValue("v3");
    const hook = mount({ save });

    type(hook, "a");
    await wait(1500);
    type(hook, "ab");
    await act(async () => window.dispatchEvent(new Event("pagehide")));
    expect(save).toHaveBeenCalledTimes(2);
    expect(save).toHaveBeenLastCalledWith("ab", { keepalive: true });

    // Reverse order: the newer text answers first.
    await act(async () => second.resolve("v2"));
    expect(hook.result.current.status).toEqual({ kind: "saving" });
    await act(async () => first.resolve("v1"));

    expect(save).toHaveBeenCalledTimes(3);
    expect(save).toHaveBeenLastCalledWith("ab", { keepalive: false });
    expect(hook.result.current.status).toEqual({ kind: "saved" });
    expect(readDraft()).toBeNull();
  });

  test("keep the draft, naming both texts, until one of them is confirmed alone", async () => {
    const first = deferred<string>();
    const second = deferred<string>();
    const save = vi
      .fn<SaveFn>()
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise)
      .mockReturnValue(new Promise(() => {}));
    const hook = mount({ save });

    type(hook, "a");
    await wait(1500);
    type(hook, "ab");
    await act(async () => window.dispatchEvent(new Event("pagehide")));
    await act(async () => second.resolve("v2"));
    await act(async () => first.resolve("v1"));
    await wait(300);

    expect(readDraft()).toMatchObject({
      text: "ab",
      sent: [textFingerprint("a"), textFingerprint("ab")],
    });
  });
});

describe("leaving the page", () => {
  test("does not send the same text twice", async () => {
    const save = vi.fn<SaveFn>().mockReturnValue(new Promise(() => {}));
    const hook = mount({ save });

    type(hook, "a");
    await wait(1500);
    type(hook, "ab");
    await act(async () => window.dispatchEvent(new Event("pagehide")));
    await act(async () => window.dispatchEvent(new Event("pagehide")));
    setVisibility("hidden");
    await act(async () => document.dispatchEvent(new Event("visibilitychange")));

    expect(save).toHaveBeenCalledTimes(2);
  });

  // Refused writes count against the shared per-minute budget, so a merely
  // hidden tab waits its turn.
  test("waits out a pending retry when the tab is only hidden", async () => {
    const save = vi.fn<SaveFn>().mockRejectedValueOnce(new TypeError("offline")).mockResolvedValue("v1");
    const hook = mount({ save });

    type(hook, "a");
    await wait(1500);
    expect(save).toHaveBeenCalledTimes(1);

    setVisibility("hidden");
    await act(async () => document.dispatchEvent(new Event("visibilitychange")));

    expect(save).toHaveBeenCalledTimes(1);
  });

  test("sends anyway when the page itself is going away", async () => {
    const save = vi.fn<SaveFn>().mockRejectedValueOnce(new TypeError("offline")).mockResolvedValue("v1");
    const hook = mount({ save });

    type(hook, "a");
    await wait(1500);
    await act(async () => window.dispatchEvent(new Event("pagehide")));

    expect(save).toHaveBeenCalledTimes(2);
    expect(save).toHaveBeenLastCalledWith("a", { keepalive: true });
  });
});

describe("the draft", () => {
  test("is written as the text changes, against the version it edits", async () => {
    const hook = mount();

    type(hook, "a");
    await wait(300);

    expect(readDraft()).toMatchObject({ text: "a", base: "v0" });
  });

  test("is removed once that exact text is saved", async () => {
    const hook = mount();

    type(hook, "a");
    await wait(1500);

    expect(readDraft()).toBeNull();
  });

  test("stays, against the new version, when the text moved on during the save", async () => {
    const first = deferred<string>();
    const save = vi.fn<SaveFn>().mockReturnValueOnce(first.promise).mockReturnValue(new Promise(() => {}));
    const hook = mount({ save });

    type(hook, "a");
    await wait(1500);
    type(hook, "ab");
    await act(async () => first.resolve("v1"));
    await wait(300);

    expect(readDraft()).toMatchObject({ text: "ab", base: "v1" });
  });

  test("names the text in flight, so a save that landed unseen does not beat it", async () => {
    const save = vi.fn<SaveFn>().mockReturnValue(new Promise(() => {}));
    const hook = mount({ save });

    type(hook, "a");
    await wait(1500);
    type(hook, "ab");
    await wait(300);

    expect(readDraft()).toEqual({ text: "ab", base: "v0", sent: [textFingerprint("a")] });
  });

  test("newer than the server copy is shown and saved at once", async () => {
    window.localStorage.setItem(KEY, JSON.stringify({ text: "typed before reload", base: "v0" }));
    const hook = mount();

    await act(async () => {});

    expect(hook.onRestore).toHaveBeenCalledWith("typed before reload");
    expect(hook.save).toHaveBeenCalledWith("typed before reload", { keepalive: false });
  });

  test("still wins when the server holds the save that was in flight when the page went away", async () => {
    window.localStorage.setItem(
      KEY,
      JSON.stringify({ text: "the later text", base: "v0", sent: [textFingerprint("server")] }),
    );
    const hook = mount({ initialVersion: "v9" });

    await act(async () => {});

    expect(hook.onRestore).toHaveBeenCalledWith("the later text");
  });

  test("loses to a server copy saved later from elsewhere, and is dropped", async () => {
    window.localStorage.setItem(KEY, JSON.stringify({ text: "old draft", base: "v0" }));
    const hook = mount({ initialVersion: "v9" });

    await act(async () => {});

    expect(hook.onRestore).not.toHaveBeenCalled();
    expect(hook.save).not.toHaveBeenCalled();
    expect(readDraft()).toBeNull();
  });

  test("equal to the server copy is simply dropped", async () => {
    window.localStorage.setItem(KEY, JSON.stringify({ text: "server", base: "v0" }));
    const hook = mount();

    await act(async () => {});

    expect(hook.onRestore).not.toHaveBeenCalled();
    expect(hook.save).not.toHaveBeenCalled();
    expect(readDraft()).toBeNull();
  });

  test("that is not a draft is ignored", async () => {
    window.localStorage.setItem(KEY, "{not json");
    const hook = mount();

    await act(async () => {});

    expect(hook.onRestore).not.toHaveBeenCalled();
    expect(hook.save).not.toHaveBeenCalled();
  });

  test("belongs to one contest and one document", () => {
    expect(draftStorageKey("u1", "c1", "notes")).not.toBe(draftStorageKey("u1", "c2", "notes"));
    expect(draftStorageKey("u1", "c1", "notes")).not.toBe(draftStorageKey("u1", "c1", "tab:1"));
  });

  // Lab machines are shared, and two accounts that never saved both stand on
  // a null version; without the account in the key, the previous user's draft
  // would be restored and autosaved into the current account.
  test("belongs to one account", () => {
    expect(draftStorageKey("u1", "c1", "notes")).not.toBe(draftStorageKey("u2", "c1", "notes"));
  });

  test("of another account is never read, even when neither has saved", async () => {
    window.localStorage.setItem(
      draftStorageKey("u2", "c1", "notes"),
      JSON.stringify({ text: "their private notes", base: null }),
    );
    const hook = mount({ initialText: "", initialVersion: null });

    await act(async () => {});

    expect(hook.onRestore).not.toHaveBeenCalled();
    expect(hook.save).not.toHaveBeenCalled();
  });

  // A leftover draft would wait for its author's next sign-in on a shared
  // machine; the screen sweeps other accounts' drafts on the way in.
  test("of another account is swept when this account opens the screen", () => {
    const mine = draftStorageKey("u1", "c1", "notes");
    const theirs = draftStorageKey("u2", "c1", "notes");
    window.localStorage.setItem(mine, JSON.stringify({ text: "mine", base: null }));
    window.localStorage.setItem(theirs, JSON.stringify({ text: "theirs", base: null }));
    window.localStorage.setItem("unrelated", "kept");

    purgeForeignDrafts("u1");

    expect(window.localStorage.getItem(mine)).not.toBeNull();
    expect(window.localStorage.getItem(theirs)).toBeNull();
    expect(window.localStorage.getItem("unrelated")).toBe("kept");
  });

  // With no known account no key is safely theirs; losing a draft costs less
  // than exposing one.
  test("is not kept at all when the account is unknown", async () => {
    const hook = mount({ accountId: null });

    type(hook, "typed");
    await wait(AUTOSAVE_DRAFT_WRITE_MS + 10);

    expect(
      Object.keys(window.localStorage).filter((key) => key.startsWith("dbcontest.play.draft.")),
    ).toEqual([]);
  });

  test("is removed, and nothing more is sent, when the document is discarded", async () => {
    const hook = mount();

    type(hook, "a");
    act(() => hook.result.current.discard());
    await wait(15_000);
    hook.unmount();

    expect(hook.save).not.toHaveBeenCalled();
    expect(readDraft()).toBeNull();
  });
});

describe("writing the draft", () => {
  // A SQL tab holds up to 64 KiB; serialising it per keystroke is waste.
  test("costs one storage write for a burst of typing, not one per keystroke", async () => {
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    const hook = mount();

    for (const text of ["a", "ab", "abc", "abcd"]) type(hook, text);
    expect(setItem).not.toHaveBeenCalled();

    await wait(300);
    expect(setItem).toHaveBeenCalledTimes(1);
    expect(readDraft()).toMatchObject({ text: "abcd" });
  });

  test("happens at once when the page is going away, before the save leaves", () => {
    const save = vi.fn<SaveFn>().mockReturnValue(new Promise(() => {}));
    const hook = mount({ save });

    type(hook, "a");
    window.dispatchEvent(new Event("pagehide"));

    expect(readDraft()).toMatchObject({ text: "a" });
  });
});

describe("an engine attached without the hook", () => {
  // The SQL tabs hold one engine per tab, so they use this helper, not the
  // hook.
  test("saves on pagehide while attached, and nothing once detached", async () => {
    const save = vi.fn<SaveFn>(async () => "v1");
    const engine = new AutosaveEngine({
      accountId: "u1",
      contestId: "c1",
      documentKey: "tab:1",
      initialText: "SELECT 1",
      initialVersion: "v0",
      save,
    });

    const detach = attachEngine(engine);
    engine.setValue("SELECT 2");
    await act(async () => window.dispatchEvent(new Event("pagehide")));
    expect(save).toHaveBeenCalledTimes(1);

    detach();
    engine.setValue("SELECT 3");
    await act(async () => window.dispatchEvent(new Event("pagehide")));
    await wait(15_000);

    expect(save).toHaveBeenCalledTimes(1);
    expect(save).toHaveBeenCalledWith("SELECT 2", { keepalive: true });
  });
});

describe("a browser whose storage refuses", () => {
  test("still saves when every storage call throws", async () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new DOMException("denied", "SecurityError");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("full", "QuotaExceededError");
    });
    vi.spyOn(Storage.prototype, "removeItem").mockImplementation(() => {
      throw new DOMException("denied", "SecurityError");
    });
    const hook = mount();

    type(hook, "a");
    await wait(1500);

    expect(hook.save).toHaveBeenCalledWith("a", { keepalive: false });
    expect(hook.result.current.status).toEqual({ kind: "saved" });
  });

  test("still saves when the storage itself cannot be reached", async () => {
    vi.spyOn(window, "localStorage", "get").mockImplementation(() => {
      throw new DOMException("denied", "SecurityError");
    });
    const hook = mount();

    type(hook, "a");
    await wait(1500);

    expect(hook.save).toHaveBeenCalledWith("a", { keepalive: false });
  });
});

describe("what re-renders", () => {
  test("only a change of status, not every keystroke", async () => {
    const hook = mount();
    const before = hook.renders();

    for (const text of ["a", "ab", "abc", "abcd", "abcde"]) type(hook, text);
    // saved -> pending
    expect(hook.renders()).toBe(before + 1);

    await wait(1500);
    // pending -> saving -> saved, at most one render each
    expect(hook.renders()).toBeLessThanOrEqual(before + 3);
  });

  test("keeps its functions stable across renders", async () => {
    const hook = mount();
    const { setValue, flush, discard } = hook.result.current;

    type(hook, "a");
    await wait(1500);

    expect(hook.result.current.setValue).toBe(setValue);
    expect(hook.result.current.flush).toBe(flush);
    expect(hook.result.current.discard).toBe(discard);
  });

  test("reports an edit waiting to be sent as pending", () => {
    const hook = mount();

    type(hook, "a");

    expect(hook.result.current.status).toEqual({ kind: "pending" });
  });
});
