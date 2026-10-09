import { renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";

import {
  PASTE_TARGET_ATTRIBUTE,
  SIGNAL_BATCH_MAX,
  SIGNAL_BUFFER_MAX,
  SIGNAL_FLUSH_MS,
  SIGNAL_PASTE_TEXT_MAX,
  SignalCollector,
  useSignals,
  type Signal,
  type SendSignals,
} from "./use-signals";

let visibility: DocumentVisibilityState = "visible";

function setVisibility(state: DocumentVisibilityState) {
  visibility = state;
  document.dispatchEvent(new Event("visibilitychange"));
}

function blur() {
  window.dispatchEvent(new Event("blur"));
}

function focus() {
  window.dispatchEvent(new Event("focus"));
}

/** A paste event carrying `text`, as a browser dispatches one. */
function pasteInto(element: Element, text: string) {
  const event = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "clipboardData", {
    value: { getData: (type: string) => (type === "text/plain" ? text : "") },
  });
  element.dispatchEvent(event);
  return event;
}

function field(target: string, tag = "textarea"): HTMLElement {
  const wrapper = document.createElement("div");
  wrapper.setAttribute(PASTE_TARGET_ATTRIBUTE, target);
  const inner = document.createElement(tag);
  wrapper.appendChild(inner);
  document.body.appendChild(wrapper);
  return inner;
}

type Sent = { events: Signal[]; keepalive: boolean };

function recorder(answer: () => Promise<void> = async () => undefined) {
  const sent: Sent[] = [];
  const send = vi.fn<SendSignals>(async (events, options) => {
    sent.push({ events: [...events], keepalive: options.keepalive });
    return answer();
  });
  return { send, sent };
}

let detach: (() => void) | undefined;

function start(send: SendSignals) {
  const collector = new SignalCollector(send);
  detach = collector.attach();
  return collector;
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-09-18T10:00:00Z"));
  visibility = "visible";
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => visibility });
});

afterEach(() => {
  detach?.();
  detach = undefined;
  document.body.innerHTML = "";
  vi.useRealTimers();
});

describe("leaving the page", () => {
  test("an absence of a second or more is recorded when the participant comes back", () => {
    const { send } = recorder();
    const collector = start(send);

    setVisibility("hidden");
    vi.advanceTimersByTime(2500);
    setVisibility("visible");

    expect(collector.pending()).toEqual([
      { kind: "page_left", away_ms: 2500, client_at: "2026-09-18T10:00:02.500Z" },
    ]);
  });

  test("an absence shorter than a second is not recorded", () => {
    const { send } = recorder();
    const collector = start(send);

    blur();
    vi.advanceTimersByTime(999);
    focus();

    expect(collector.pending()).toEqual([]);
  });

  test("losing focus counts as leaving too", () => {
    const { send } = recorder();
    const collector = start(send);

    blur();
    vi.advanceTimersByTime(4000);
    focus();

    expect(collector.pending()).toMatchObject([{ kind: "page_left", away_ms: 4000 }]);
  });

  test("a hide and a blur that overlap are one absence", () => {
    const { send } = recorder();
    const collector = start(send);

    blur();
    vi.advanceTimersByTime(1000);
    setVisibility("hidden");
    vi.advanceTimersByTime(3000);
    setVisibility("visible");
    vi.advanceTimersByTime(500);
    focus();

    expect(collector.pending()).toMatchObject([{ kind: "page_left", away_ms: 4000 }]);
  });
});

describe("pasting", () => {
  test.each(["editor", "answer", "notes"])("a paste into the %s is recorded with its size and beginning", (target) => {
    const { send } = recorder();
    const collector = start(send);

    const event = pasteInto(field(target), "SELECT * FROM suspects");

    expect(collector.pending()).toEqual([
      { kind: "paste", target, chars: 22, text: "SELECT * FROM suspects", client_at: "2026-09-18T10:00:00.000Z" },
    ]);
    // Watching a paste does not stop it.
    expect(event.defaultPrevented).toBe(false);
  });

  test("only the first 500 characters are kept, and the count is the whole paste", () => {
    const { send } = recorder();
    const collector = start(send);

    pasteInto(field("notes"), "x".repeat(2000));

    const [paste] = collector.pending() as Extract<Signal, { kind: "paste" }>[];
    expect(paste.chars).toBe(2000);
    expect(paste.text).toHaveLength(SIGNAL_PASTE_TEXT_MAX);
  });

  test("a cut never splits a character in two", () => {
    const { send } = recorder();
    const collector = start(send);

    pasteInto(field("notes"), "x".repeat(SIGNAL_PASTE_TEXT_MAX - 1) + "😀tail");

    const [paste] = collector.pending() as Extract<Signal, { kind: "paste" }>[];
    expect(paste.text).toBe("x".repeat(SIGNAL_PASTE_TEXT_MAX - 1));
  });

  test("a paste anywhere else, or of nothing, is not recorded", () => {
    const { send } = recorder();
    const collector = start(send);

    const elsewhere = document.createElement("input");
    document.body.appendChild(elsewhere);
    pasteInto(elsewhere, "hello");
    pasteInto(field("notes"), "");

    expect(collector.pending()).toEqual([]);
  });
});

describe("sending", () => {
  test("the buffer is sent every ten seconds, and only when there is something in it", async () => {
    const { send, sent } = recorder();
    start(send);

    await vi.advanceTimersByTimeAsync(SIGNAL_FLUSH_MS);
    expect(send).not.toHaveBeenCalled();

    pasteInto(field("answer"), "42");
    await vi.advanceTimersByTimeAsync(SIGNAL_FLUSH_MS);

    expect(sent).toHaveLength(1);
    expect(sent[0]).toMatchObject({ keepalive: false, events: [{ kind: "paste", target: "answer" }] });

    await vi.advanceTimersByTimeAsync(SIGNAL_FLUSH_MS);
    expect(send).toHaveBeenCalledTimes(1);
  });

  test("hiding the page sends at once, with keepalive", async () => {
    const { send, sent } = recorder();
    start(send);

    pasteInto(field("notes"), "clue");
    setVisibility("hidden");
    await vi.advanceTimersByTimeAsync(0);

    expect(sent).toEqual([{ keepalive: true, events: [expect.objectContaining({ kind: "paste" })] }]);
  });

  test("the page going away sends at once, with keepalive", async () => {
    const { send, sent } = recorder();
    start(send);

    pasteInto(field("editor"), "SELECT 1");
    window.dispatchEvent(new Event("pagehide"));
    await vi.advanceTimersByTimeAsync(0);

    expect(sent).toEqual([{ keepalive: true, events: [expect.objectContaining({ kind: "paste", target: "editor" })] }]);
  });

  test("a batch carries at most fifty signals", async () => {
    const { send, sent } = recorder();
    const collector = start(send);
    const notes = field("notes");

    for (let i = 0; i < SIGNAL_BATCH_MAX + 5; i++) pasteInto(notes, `p${i}`);
    await vi.advanceTimersByTimeAsync(SIGNAL_FLUSH_MS);

    expect(sent[0].events).toHaveLength(SIGNAL_BATCH_MAX);
    expect(collector.pending()).toHaveLength(5);
    await vi.advanceTimersByTimeAsync(SIGNAL_FLUSH_MS);
    expect(sent[1].events).toHaveLength(5);
  });

  test("a keepalive batch stays under the browser's 64 KiB keepalive budget", async () => {
    const { send, sent } = recorder();
    start(send);
    const notes = field("notes");

    // Fifty pastes of 500 three-byte characters: about 75 KiB.
    for (let i = 0; i < SIGNAL_BATCH_MAX; i++) pasteInto(notes, "я".repeat(SIGNAL_PASTE_TEXT_MAX));
    window.dispatchEvent(new Event("pagehide"));
    await vi.advanceTimersByTimeAsync(0);

    const bytes = new TextEncoder().encode(JSON.stringify({ events: sent[0].events })).length;
    expect(bytes).toBeLessThan(64 * 1024);
    expect(sent[0].events.length).toBeGreaterThan(0);
  });

  test("a batch refused for the rate is kept and sent again later", async () => {
    let refuse = true;
    const { send, sent } = recorder(async () => {
      if (refuse) throw new ApiError("signals_too_often", 429, "slow down", undefined, undefined, undefined, 30);
    });
    const collector = start(send);

    pasteInto(field("notes"), "one");
    await vi.advanceTimersByTimeAsync(SIGNAL_FLUSH_MS);
    expect(sent).toHaveLength(1);
    expect(collector.pending()).toHaveLength(1);

    // Retry-After is honoured: nothing leaves before it has passed.
    refuse = false;
    await vi.advanceTimersByTimeAsync(SIGNAL_FLUSH_MS * 2);
    expect(sent).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(SIGNAL_FLUSH_MS);
    expect(sent).toHaveLength(2);
    expect(sent[1].events).toMatchObject([{ kind: "paste", text: "one" }]);
    expect(collector.pending()).toEqual([]);
  });

  test("a batch lost on the network is kept, in order, ahead of what came after", async () => {
    let fail = true;
    const { send, sent } = recorder(async () => {
      if (fail) throw new TypeError("Failed to fetch");
    });
    const collector = start(send);
    const notes = field("notes");

    pasteInto(notes, "first");
    await vi.advanceTimersByTimeAsync(SIGNAL_FLUSH_MS);
    pasteInto(notes, "second");
    expect((collector.pending() as Extract<Signal, { kind: "paste" }>[]).map((s) => s.text)).toEqual([
      "first",
      "second",
    ]);

    fail = false;
    await vi.advanceTimersByTimeAsync(SIGNAL_FLUSH_MS);
    expect(sent.at(-1)?.events.map((s) => (s as Extract<Signal, { kind: "paste" }>).text)).toEqual([
      "first",
      "second",
    ]);
  });

  test("the buffer never grows past its cap: the oldest signals go first", async () => {
    const { send } = recorder(async () => {
      throw new TypeError("Failed to fetch");
    });
    const collector = start(send);
    const notes = field("notes");

    for (let i = 0; i < SIGNAL_BUFFER_MAX + 30; i++) {
      pasteInto(notes, `p${i}`);
      if (i % 40 === 0) await vi.advanceTimersByTimeAsync(SIGNAL_FLUSH_MS);
    }
    await vi.advanceTimersByTimeAsync(SIGNAL_FLUSH_MS);

    const pending = collector.pending() as Extract<Signal, { kind: "paste" }>[];
    expect(pending).toHaveLength(SIGNAL_BUFFER_MAX);
    expect(pending[0].text).toBe("p30");
    expect(pending.at(-1)?.text).toBe(`p${SIGNAL_BUFFER_MAX + 29}`);
  });

  test("a batch the server refuses as such is dropped, not retried", async () => {
    const { send } = recorder(async () => {
      throw new ApiError("signals_batch_too_large", 400, "too large");
    });
    const collector = start(send);

    pasteInto(field("notes"), "one");
    await vi.advanceTimersByTimeAsync(SIGNAL_FLUSH_MS);

    expect(collector.pending()).toEqual([]);
    await vi.advanceTimersByTimeAsync(SIGNAL_FLUSH_MS);
    expect(send).toHaveBeenCalledTimes(1);
  });

  test.each([
    ["contest_finished", 409],
    ["contest_ended", 409],
    ["not_a_participant", 403],
    // The session ended; every later batch would be refused alike.
    ["unauthenticated", 401],
  ])("%s stops the collector for good", async (code, status) => {
    const { send } = recorder(async () => {
      throw new ApiError(code, status, "closed");
    });
    const collector = start(send);
    const notes = field("notes");

    pasteInto(notes, "one");
    await vi.advanceTimersByTimeAsync(SIGNAL_FLUSH_MS);

    pasteInto(notes, "two");
    blur();
    vi.advanceTimersByTime(5000);
    focus();
    setVisibility("hidden");
    await vi.advanceTimersByTimeAsync(SIGNAL_FLUSH_MS * 3);

    expect(collector.pending()).toEqual([]);
    expect(send).toHaveBeenCalledTimes(1);
  });

  test("detaching stops listening and stops the timer", async () => {
    const { send } = recorder();
    const collector = start(send);
    detach?.();
    detach = undefined;

    pasteInto(field("notes"), "late");
    await vi.advanceTimersByTimeAsync(SIGNAL_FLUSH_MS * 2);

    expect(collector.pending()).toEqual([]);
    expect(send).not.toHaveBeenCalled();
  });
});

describe("the edges of leaving and sending", () => {
  test("an absence the page never comes back from is recorded when the page goes away", async () => {
    const { send, sent } = recorder();
    start(send);

    setVisibility("hidden");
    vi.advanceTimersByTime(7000);
    window.dispatchEvent(new Event("pagehide"));
    await vi.advanceTimersByTimeAsync(0);

    expect(sent.at(-1)).toEqual({
      keepalive: true,
      events: [{ kind: "page_left", away_ms: 7000, client_at: "2026-09-18T10:00:07.000Z" }],
    });
  });

  test("an absence shorter than a second is not recorded when the page goes away either", async () => {
    const { send } = recorder();
    const collector = start(send);

    blur();
    vi.advanceTimersByTime(400);
    window.dispatchEvent(new Event("pagehide"));
    await vi.advanceTimersByTimeAsync(0);

    expect(send).not.toHaveBeenCalled();
    expect(collector.pending()).toEqual([]);
  });

  test("switching tabs quickly does not spend a batch on every hide", async () => {
    const { send, sent } = recorder();
    const collector = start(send);
    const notes = field("notes");

    pasteInto(notes, "one");
    setVisibility("hidden");
    await vi.advanceTimersByTimeAsync(0);
    expect(sent).toHaveLength(1);

    // Back after 2 s, away again 1 s later: too soon after the last batch to
    // send one.
    vi.advanceTimersByTime(2000);
    setVisibility("visible");
    vi.advanceTimersByTime(1000);
    setVisibility("hidden");
    await vi.advanceTimersByTimeAsync(0);
    expect(sent).toHaveLength(1);
    expect(collector.pending()).toMatchObject([{ kind: "page_left", away_ms: 2000 }]);

    // The timer still sends it.
    await vi.advanceTimersByTimeAsync(SIGNAL_FLUSH_MS);
    expect(sent).toHaveLength(2);
  });

  test("two batches that fail together go back in the order they happened", async () => {
    const pending: Array<(error: unknown) => void> = [];
    const send = vi.fn<SendSignals>(
      () =>
        new Promise<void>((_, reject) => {
          pending.push(reject);
        }),
    );
    const collector = start(send);
    const notes = field("notes");

    pasteInto(notes, "first");
    await vi.advanceTimersByTimeAsync(SIGNAL_FLUSH_MS);
    vi.advanceTimersByTime(1000);
    pasteInto(notes, "second");
    window.dispatchEvent(new Event("pagehide"));
    await vi.advanceTimersByTimeAsync(0);
    expect(send).toHaveBeenCalledTimes(2);

    // The older batch fails first, the newer one after it.
    pending[0](new TypeError("Failed to fetch"));
    await vi.advanceTimersByTimeAsync(0);
    pending[1](new TypeError("Failed to fetch"));
    await vi.advanceTimersByTimeAsync(0);

    expect((collector.pending() as Extract<Signal, { kind: "paste" }>[]).map((s) => s.text)).toEqual([
      "first",
      "second",
    ]);
  });
});

describe("refusals that may pass and pages that come back", () => {
  // A laptop briefly on a hotspot must not silence monitoring until a
  // reload.
  test("address_not_allowed drops the batch and keeps collecting", async () => {
    let refuse = true;
    const { send, sent } = recorder(async () => {
      if (refuse) throw new ApiError("address_not_allowed", 403, "not from here");
    });
    const collector = start(send);
    const notes = field("notes");

    pasteInto(notes, "one");
    await vi.advanceTimersByTimeAsync(SIGNAL_FLUSH_MS);
    expect(collector.pending()).toEqual([]);

    refuse = false;
    pasteInto(notes, "two");
    await vi.advanceTimersByTimeAsync(SIGNAL_FLUSH_MS);
    expect(sent).toHaveLength(2);
    expect(sent[1].events).toMatchObject([{ kind: "paste", text: "two" }]);
  });

  test("a page restored from the back/forward cache records the time it was gone", () => {
    const { send } = recorder();
    const collector = start(send);
    const transition = (type: string) => {
      const event = new Event(type);
      Object.defineProperty(event, "persisted", { value: true });
      window.dispatchEvent(event);
    };

    transition("pagehide");
    vi.advanceTimersByTime(5000);
    transition("pageshow");

    expect(collector.pending()).toMatchObject([{ kind: "page_left", away_ms: 5000 }]);
  });
});

describe("useSignals", () => {
  test("collects for the contest without re-rendering the screen on any signal", async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    let renders = 0;
    const hook = renderHook(() => {
      renders++;
      useSignals("c 1");
    });

    const notes = field("notes");
    for (let i = 0; i < 5; i++) pasteInto(notes, `p${i}`);
    blur();
    vi.advanceTimersByTime(3000);
    focus();
    await vi.advanceTimersByTimeAsync(SIGNAL_FLUSH_MS);

    expect(renders).toBe(1);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/contests/c%201/play/signals");
    expect(init).toMatchObject({ method: "POST", credentials: "same-origin", keepalive: false });
    expect(JSON.parse(String(init.body)).events).toHaveLength(6);

    hook.unmount();
    vi.unstubAllGlobals();
  });
});
