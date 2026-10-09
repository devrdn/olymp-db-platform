import { activeDictionary } from "@/lib/i18n/server";

import { playDictionary } from "./dictionary";
import { PlayHeaderSkeleton, WorkspaceSkeleton } from "./skeleton";

/**
 * What is on screen between the click and the workspace; Next renders it
 * before the route's first `await`. The header is a skeleton only here, since
 * the contest's title is not known yet.
 *
 * It also keeps a prefetch from starting a clock: under individual timing the
 * page's reads start it, and with this boundary a prefetched `<Link>` renders
 * only down to this file, not the page.
 *
 * No `Band`: the workspace is full-bleed (SPEC.md §5's exception), and the
 * skeleton must match it.
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
