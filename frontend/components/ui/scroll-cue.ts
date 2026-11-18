/**
 * The script half of the `scroll-cue` utility (globals.css), which fades an
 * edge of a horizontal row while content is hidden past it. Pass it as the
 * row's `ref`, beside the class:
 *
 *     <nav ref={scrollCue} className="scroll-cue overflow-x-auto">
 *
 * Where the browser has scroll-driven animations the stylesheet follows the
 * scroll position by itself and this does nothing. Elsewhere it marks the
 * edges with `data-cue-start` and `data-cue-end` for the same rule to read;
 * without either, nothing is faded, so the cue can only ever be missing, never
 * hide what it marks.
 */
export function scrollCue(row: HTMLElement | null): (() => void) | undefined {
  if (!row || followsScrollInCss()) return;

  const sync = () => {
    const hidden = row.scrollWidth - row.clientWidth;
    // A pixel of slack: a zoomed page reports offsets that stop just short of
    // either end.
    row.toggleAttribute("data-cue-start", row.scrollLeft > 1);
    row.toggleAttribute("data-cue-end", row.scrollLeft < hidden - 1);
  };
  sync();

  row.addEventListener("scroll", sync, { passive: true });
  // The box changes with the viewport and the panes; the content with the
  // locale and with tabs that come and go.
  const resized = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(sync);
  resized?.observe(row);
  const changed = new MutationObserver(sync);
  changed.observe(row, { childList: true, characterData: true, subtree: true });

  return () => {
    row.removeEventListener("scroll", sync);
    resized?.disconnect();
    changed.disconnect();
  };
}

function followsScrollInCss(): boolean {
  return typeof CSS !== "undefined" && typeof CSS.supports === "function" && CSS.supports("animation-timeline: scroll()");
}
