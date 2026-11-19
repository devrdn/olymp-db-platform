import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";
import type { PlayDictionary } from "./dictionary";

/**
 * The height of the play screen under the app bar: one viewport less the
 * bar's `h-12` and its 1px rule, so the page itself does not scroll and every
 * pane scrolls inside.
 *
 * A phone on its side is too short for that: the header would leave the pane
 * a sliver. There the shell is as tall as its content and `PLAY_PANES`, the
 * part under the header, takes the viewport instead, so the header scrolls
 * away and the switch and its pane then fill the screen exactly.
 */
export const PLAY_SHELL = "h-[calc(100dvh-3rem-1px)] phone-short:h-auto";
export const PLAY_PANES = "min-h-0 flex-1 phone-short:h-[calc(100dvh-3rem-1px)] phone-short:flex-none";

/**
 * Stands in for the workspace while it loads, both in `loading.tsx` (before
 * the page renders at all) and inside the page's `<Suspense>`; without it Next
 * keeps the previous page on screen, or a white one on a hard reload, for
 * seconds when a round opens. Built on the same grid as `workspace.tsx`
 * (SPEC.md §7: a skeleton shaped like the content), so nothing moves when the
 * panes arrive: below `narrow` that is the switch and the editor, the pane a
 * phone opens on. The shapes are `aria-hidden`; one `role="status"` announces
 * the wait.
 */
export function WorkspaceSkeleton({ dict }: { dict: PlayDictionary }) {
  return (
    <div className={cn(PLAY_PANES, "flex flex-col print:hidden")}>
      <div aria-hidden data-skeleton="switch" className="min-h-11 shrink-0 border-b border-line narrow:hidden" />
      <div
        aria-hidden
        className="grid min-h-0 flex-1 grid-cols-1 max-narrow:grid-rows-[minmax(0,1fr)] narrow:grid-cols-[minmax(0,1fr)_1px_15.75rem] wide:grid-cols-[13.25rem_1px_minmax(0,1fr)_1px_15.75rem]"
      >
        {/* The schema pane, placed as the real grid places it. */}
        <div
          data-skeleton="schema"
          className="flex min-h-0 flex-col gap-3 border-line p-3 max-wide:col-span-full max-narrow:hidden narrow:max-wide:order-4 narrow:max-wide:max-h-80 narrow:max-wide:border-t"
        >
          <Skeleton className="h-3 w-24" />
          {[0, 1, 2, 3, 4].map((row) => (
            <Skeleton key={row} className="h-3.5 w-full max-w-40" />
          ))}
        </div>
        <div className="bg-line max-wide:hidden" />

        {/* The console column: editor above, result below, 11:9. */}
        <div className="grid min-h-0 grid-cols-1 grid-rows-[minmax(0,11fr)_minmax(0,9fr)] border-line max-wide:order-1 max-narrow:grid-rows-[minmax(0,1fr)]">
          <div
            data-skeleton="editor"
            className="flex min-h-0 flex-col gap-3 border-b border-line p-3 max-narrow:border-b-0"
          >
            <div className="flex items-center gap-2">
              <Skeleton className="h-7 w-16" />
              <div className="flex-1" />
              <Skeleton className="h-6 w-20" />
              <Skeleton className="h-6 w-24" />
            </div>
            <Skeleton className="h-3.5 w-72 max-w-full rounded-none" />
            <Skeleton className="h-3.5 w-56 max-w-full rounded-none" />
          </div>
          <div data-skeleton="result" className="flex min-h-0 flex-col gap-2 p-3 max-narrow:hidden">
            <Skeleton className="h-3 w-40" />
            {[0, 1, 2, 3, 4, 5].map((row) => (
              <Skeleton key={row} className="h-3.5 w-full rounded-none" />
            ))}
          </div>
        </div>

        <div className="bg-line max-narrow:hidden max-wide:order-2" />

        {/* The story and questions pane. */}
        <div data-skeleton="side" className="flex min-h-0 flex-col gap-3 border-line p-3 max-wide:order-3 max-narrow:hidden">
          <div className="flex gap-4">
            <Skeleton className="h-3 w-16" />
            <Skeleton className="h-3 w-24" />
          </div>
          {[0, 1, 2, 3, 4, 5, 6].map((row) => (
            <Skeleton key={row} className="h-3.5 w-full rounded-none" />
          ))}
        </div>
      </div>

      <span role="status" className="sr-only">
        {dict.participant.play.loading}
      </span>
    </div>
  );
}

/**
 * The header's stand-in, for `loading.tsx` only, before the contest's name is
 * known. Inside the page the real `PlayHeader` renders outside the Suspense
 * boundary.
 */
export function PlayHeaderSkeleton() {
  return (
    <div
      aria-hidden
      className="flex shrink-0 flex-wrap items-center justify-between gap-3 border-b border-line bg-bg px-4 py-2.5"
    >
      <Skeleton className="h-5 w-64 max-w-[60%] rounded-none" />
      <Skeleton className="h-5 w-24" />
    </div>
  );
}
