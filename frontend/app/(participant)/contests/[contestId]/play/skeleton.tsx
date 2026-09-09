import { Skeleton } from "@/components/ui/skeleton";
import type { PlayDictionary } from "./dictionary";

/**
 * What stands in for the workspace while it is still being built.
 *
 * Finding 2: this screen had neither a `loading.tsx` nor a Suspense
 * boundary, and the two are different waits. Next holds the *previous* page
 * on screen for the whole of a server render, so a participant who pressed
 * "Enter the contest" saw the register they had just left, unchanged, with
 * nothing to say a thing had happened — 100–200ms idle, seconds at the
 * minute a tour opens and three hundred people ask the API for a story, a
 * question list, a query log and a schema at once. On a hard reload the same
 * wait is a white screen. `loading.tsx` answers the first wait and this
 * component's own use inside `<Suspense>` answers the second, which is why
 * the shapes live here once rather than twice.
 *
 * Shaped like the workspace rather than spun: SPEC.md §7 asks the `loading`
 * state for "a skeleton shaped like the content to come", and this one is
 * built from the same three-pane grid `workspace.tsx` renders, so the
 * console, the result panel and the questions do not move when they arrive.
 *
 * A skeleton is `aria-hidden` (see `Skeleton`), so the wait is announced in
 * words beside it — one `role="status"`, not one per shape.
 */
export function WorkspaceSkeleton({ dict }: { dict: PlayDictionary }) {
  return (
    <div className="flex min-h-0 flex-col print:hidden narrow:flex-1">
      <div
        aria-hidden
        className="grid min-h-0 grid-cols-1 narrow:flex-1 narrow:grid-cols-[minmax(0,1fr)_1px_15.75rem] wide:grid-cols-[13.25rem_1px_minmax(0,1fr)_1px_15.75rem]"
      >
        {/* The schema pane: below the console until there is room beside it,
            the same order the real grid places it in. */}
        <div className="flex min-h-0 flex-col gap-3 border-line p-3 max-wide:col-span-full max-wide:max-h-80 max-wide:border-t max-narrow:order-2 narrow:max-wide:order-4">
          <Skeleton className="h-3 w-24" />
          {[0, 1, 2, 3, 4].map((row) => (
            <Skeleton key={row} className="h-3.5 w-full max-w-40" />
          ))}
        </div>
        <div className="bg-line max-wide:hidden" />

        {/* The console column: the editor above, its result below, in the
            same 11/9 split. */}
        <div className="grid min-h-0 grid-cols-1 grid-rows-[minmax(0,11fr)_minmax(0,9fr)] border-line max-wide:order-1 max-narrow:grid-rows-none max-narrow:border-b">
          <div className="flex min-h-0 flex-col gap-3 border-b border-line p-3 max-narrow:min-h-80">
            <div className="flex items-center gap-2">
              <Skeleton className="h-7 w-16" />
              <div className="flex-1" />
              <Skeleton className="h-6 w-20" />
              <Skeleton className="h-6 w-24" />
            </div>
            <Skeleton className="h-3.5 w-72 max-w-full rounded-none" />
            <Skeleton className="h-3.5 w-56 max-w-full rounded-none" />
          </div>
          <div className="flex min-h-0 flex-col gap-2 p-3 max-narrow:max-h-[60svh]">
            <Skeleton className="h-3 w-40" />
            {[0, 1, 2, 3, 4, 5].map((row) => (
              <Skeleton key={row} className="h-3.5 w-full rounded-none" />
            ))}
          </div>
        </div>

        <div className="bg-line max-narrow:hidden max-wide:order-2" />

        {/* The story and questions pane. */}
        <div className="flex min-h-0 flex-col gap-3 border-line p-3 max-wide:order-3 max-narrow:min-h-100 max-narrow:border-t">
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
 * The bar above it, for the one wait where even the contest's name is not
 * known yet — `loading.tsx`, which runs before the page has read the
 * participant's own enrolled listing.
 *
 * Inside the page itself the real `PlayHeader` is rendered instead, outside
 * the Suspense boundary: by then the title and the clock *are* known, and
 * handing a participant a live countdown in the first wave is the whole
 * point of splitting the boundary there (finding 2).
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
