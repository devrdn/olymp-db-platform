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
  // A web font that lands after the first layout changes the labels' widths
  // and nothing either observer watches.
  let followed = true;
  document.fonts?.ready.then(() => {
    if (followed) sync();
  });

  return () => {
    followed = false;
    row.removeEventListener("scroll", sync);
    resized?.disconnect();
    changed.disconnect();
  };
}

/**
 * What the stylesheet needs to follow the scroll itself, and the condition of
 * its `@supports` rule (globals.css). A browser with a timeline but no ranges
 * or no `auto` duration would fade the start of a row that has not moved.
 */
export const SCROLL_DRIVEN_CUE =
  "(animation-timeline: scroll()) and (animation-range: 0 100%) and (animation-duration: auto)";

function followsScrollInCss(): boolean {
  return typeof CSS !== "undefined" && typeof CSS.supports === "function" && CSS.supports(SCROLL_DRIVEN_CUE);
}
