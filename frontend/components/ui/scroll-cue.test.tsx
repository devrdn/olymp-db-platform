import { readFileSync } from "node:fs";
import path from "node:path";

import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";

import { SCROLL_DRIVEN_CUE, scrollCue } from "./scroll-cue";
import { cuedEdges, measureScroller, scrollRowTo } from "./scroll-cue-test-support";

function Row({ items = ["Story", "Questions", "Notes"] }: { items?: string[] }) {
  return (
    <div data-testid="row" ref={scrollCue} className="scroll-cue overflow-x-auto">
      {items.map((item) => (
        <span key={item}>{item}</span>
      ))}
    </div>
  );
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("the scroll cue", () => {
  test("fades the far edge of a row wider than its box, and not the edge it starts at", () => {
    measureScroller({ scrollWidth: 446, clientWidth: 375 });
    render(<Row />);

    expect(cuedEdges(screen.getByTestId("row"))).toEqual(["end"]);
  });

  test("fades nothing when the row fits", () => {
    measureScroller({ scrollWidth: 375, clientWidth: 375 });
    render(<Row />);

    expect(cuedEdges(screen.getByTestId("row"))).toEqual([]);
  });

  test("fades both edges part way along, and drops the far one at the end", () => {
    measureScroller({ scrollWidth: 446, clientWidth: 375 });
    render(<Row />);
    const row = screen.getByTestId("row");

    scrollRowTo(row, 30);
    expect(cuedEdges(row)).toEqual(["start", "end"]);

    scrollRowTo(row, 71);
    expect(cuedEdges(row)).toEqual(["start"]);

    scrollRowTo(row, 0);
    expect(cuedEdges(row)).toEqual(["end"]);
  });

  // A zoomed page reports a fractional offset that stops short of the end.
  test("counts a row within a pixel of its end as at the end", () => {
    measureScroller({ scrollWidth: 446, clientWidth: 375 });
    render(<Row />);
    const row = screen.getByTestId("row");

    scrollRowTo(row, 70.4);

    expect(cuedEdges(row)).toEqual(["start"]);
  });

  test("measures again when the row's content changes", async () => {
    measureScroller({ scrollWidth: 375, clientWidth: 375 });
    const { rerender } = render(<Row />);
    const row = screen.getByTestId("row");
    expect(cuedEdges(row)).toEqual([]);

    measureScroller({ scrollWidth: 520, clientWidth: 375 });
    rerender(<Row items={["Story", "Tables", "Questions", "Organisers", "Notes", "Table"]} />);

    await vi.waitFor(() => expect(cuedEdges(row)).toEqual(["end"]));
  });

  test("stops following the row once it is gone", () => {
    measureScroller({ scrollWidth: 446, clientWidth: 375 });
    const { unmount } = render(<Row />);
    const row = screen.getByTestId("row");
    unmount();

    scrollRowTo(row, 30);

    expect(cuedEdges(row)).toEqual(["end"]);
  });

  // There the stylesheet follows the scroll position by itself, before any script.
  test("leaves the row to the stylesheet where the browser has scroll-driven animations", () => {
    vi.stubGlobal("CSS", { supports: (query: string) => query === SCROLL_DRIVEN_CUE });
    measureScroller({ scrollWidth: 446, clientWidth: 375 });
    render(<Row />);
    const row = screen.getByTestId("row");

    scrollRowTo(row, 30);

    expect(cuedEdges(row)).toEqual([]);
  });

  // A timeline without ranges or an `auto` duration would fade the start edge
  // of a row that has not moved, so the stylesheet does not take it either.
  test("marks the edges itself where the browser has scroll timelines but not the rest", () => {
    vi.stubGlobal("CSS", { supports: (query: string) => query === "animation-timeline: scroll()" });
    measureScroller({ scrollWidth: 446, clientWidth: 375 });
    render(<Row />);

    expect(cuedEdges(screen.getByTestId("row"))).toEqual(["end"]);
  });
});

/** The rule itself, read from globals.css: jsdom applies no stylesheet. */
describe("the scroll-cue utility", () => {
  const css = readFileSync(path.resolve(__dirname, "../../app/globals.css"), "utf8");
  const utility = css.slice(css.indexOf("@utility scroll-cue"), css.indexOf("@keyframes scroll-cue-start"));

  test("animates under the same condition the script tests, so exactly one of them runs", () => {
    expect(utility).toContain(`@supports ${SCROLL_DRIVEN_CUE} {`);
  });

  // Where `@property` is missing, an unset width would void the whole mask.
  test("reads each fade width with a fallback of no fade", () => {
    const reads = utility.match(/var\(--scroll-cue-(?:start|end)[^)]*\)/g) ?? [];

    expect(reads.length).toBeGreaterThan(0);
    for (const read of reads) expect(read).toMatch(/, 0px\)$/);
  });

  // `scrollIntoView` and focus would otherwise park a tab, or the dot on it,
  // under the fade it is being brought out of.
  test("keeps what is scrolled into view clear of the fade", () => {
    expect(utility).toContain("scroll-padding-inline: var(--scroll-cue);");
  });
});
