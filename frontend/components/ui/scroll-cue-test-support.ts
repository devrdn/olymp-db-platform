/**
 * Giving a horizontal scroller a size, for the tests of `scrollCue` and of the
 * rows that use it: jsdom lays nothing out, so every width reads as zero.
 */
import { fireEvent } from "@testing-library/react";
import { vi } from "vitest";

/**
 * Every element reports these widths from now on. Called before the render,
 * since the cue measures as soon as the row mounts; `vi.restoreAllMocks`
 * undoes it.
 */
export function measureScroller({ scrollWidth, clientWidth }: { scrollWidth: number; clientWidth: number }) {
  vi.spyOn(Element.prototype, "scrollWidth", "get").mockReturnValue(scrollWidth);
  vi.spyOn(Element.prototype, "clientWidth", "get").mockReturnValue(clientWidth);
}

/** Scrolls the row to `left` pixels from its start, as a finger would. */
export function scrollRowTo(row: HTMLElement, left: number) {
  Object.defineProperty(row, "scrollLeft", { configurable: true, value: left });
  fireEvent.scroll(row);
}

/** The edges that currently show a fade, as the CSS reads them. */
export function cuedEdges(row: HTMLElement): string[] {
  return (["start", "end"] as const).filter((edge) => row.hasAttribute(`data-cue-${edge}`));
}
