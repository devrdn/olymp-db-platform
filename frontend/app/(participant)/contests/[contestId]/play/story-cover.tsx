import { DrawnCover } from "@/components/product/drawn-cover";
import { coverHref } from "@/lib/api/contests";

import type { PlayDictionary } from "./dictionary";

/**
 * The picture above the crime story (SPEC.md §10), where atmosphere is
 * part of the task.
 *
 * Rendered on the server by `page.tsx` and handed down as a node, keeping
 * `DrawnCover` out of the client bundle of this screen. Not in the print
 * copy: a full-bleed photograph would spend a page of ink and push the story
 * to the second sheet.
 */

/**
 * The larger stored rendition (cards take the 800 one). The size is also
 * written onto the element so the browser reserves the box and the story
 * does not shift under a reader on the clock.
 */
const STORY_COVER = { size: 1600, height: 900 } as const;

export function StoryCover({
  contestId,
  title,
  coverHash,
  coverAttribution,
  dict,
}: {
  contestId: string;
  /** The contest's name, in the language this screen was served in. */
  title: string;
  /** The uploaded picture's hash, or empty for a drawn cover. */
  coverHash: string;
  /** Who made the uploaded picture; empty for a drawn cover. */
  coverAttribution: string;
  dict: PlayDictionary;
}) {
  const t = dict.participant.play.workspace.story;

  return (
    <div className="mb-5">
      <div className="relative aspect-video w-full overflow-hidden rounded-frame">
        {coverHash ? (
          /* Not next/image: the API already crops, resizes, re-encodes and
             caches these bytes for a year at a hashed address. */
          /* eslint-disable-next-line @next/next/no-img-element */
          <img
            src={coverHref(contestId, coverHash, STORY_COVER.size)}
            alt={t.coverOf.replace("{title}", title)}
            loading="lazy"
            decoding="async"
            width={STORY_COVER.size}
            height={STORY_COVER.height}
            className="photograph size-full object-cover"
          />
        ) : (
          /* SPEC.md §10.3: a contest with no photograph gets a drawn cover of the same
             shape, derived from its id so it looks the same on every visit. */
          <DrawnCover seed={contestId} />
        )}

        {/* SPEC.md §10.2: the title sits on a scrim resolving to the page's ground, so
            its contrast does not depend on the organiser's picture. */}
        <div
          aria-hidden
          className="absolute inset-0 bg-linear-to-t from-scrim-a from-0% via-scrim-b via-38% to-transparent to-76%"
        />

        <h2 className="absolute inset-x-0 bottom-0 px-4 pb-3 text-h3 text-ink">{title}</h2>
      </div>

      {/* The credit, required by the publish gate (SPEC.md §10.1) for an uploaded
          picture; a drawn cover has none. */}
      {coverHash && coverAttribution ? <p className="mt-2 text-small text-ink-3">{coverAttribution}</p> : null}
    </div>
  );
}
