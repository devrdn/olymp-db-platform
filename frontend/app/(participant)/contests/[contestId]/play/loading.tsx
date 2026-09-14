import { activeDictionary } from "@/lib/i18n/server";

import { playDictionary } from "./dictionary";
import { PlayHeaderSkeleton, WorkspaceSkeleton } from "./skeleton";

/**
 * What is on screen between the click and the workspace (finding 2).
 *
 * `/my`, `/open`, `/audit`, `/users` and `/contests` all had one of these;
 * the one screen hundreds of people enter within the same minute did not, so
 * a participant pressing "Enter" watched the page they were leaving sit
 * there — and a hard reload showed nothing at all — for as long as the
 * server took. Next renders this the instant navigation starts, which is
 * before this route's own first `await`.
 *
 * The header is a skeleton here and only here: this runs before the page has
 * read which contest this is, so there is no title to show and no deadline
 * to count down from. A moment later the page has both, and renders the real
 * `PlayHeader` above its own Suspense boundary.
 *
 * It also keeps a prefetch from starting anybody's clock. Under individual
 * timing the page's own reads of the story and the questions are what start
 * a participant's clock, and a `<Link>` to this route is prefetched as soon
 * as it scrolls into view. With a loading boundary here, Next prefetches only
 * down to this file and renders the page itself on the real navigation;
 * without one, merely showing a link to the workspace could render the page
 * in the background and start the clock before the participant chose to.
 *
 * No `Band`: the workspace is the one route in the product that goes
 * full-bleed (SPEC.md §5's named exception), and a loading state that keeps
 * the hatched fields would put the skeleton in a narrower column than the
 * thing it stands in for.
 */
export default async function Loading() {
  const dict = playDictionary(await activeDictionary());

  return (
    <div className="flex min-h-0 flex-col narrow:h-[calc(100dvh-3rem-1px)]">
      <PlayHeaderSkeleton />
      <WorkspaceSkeleton dict={dict} />
    </div>
  );
}
