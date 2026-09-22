import { DrawnCover } from "@/components/product/drawn-cover";
import { coverHref } from "@/lib/api/contests";

import type { PlayDictionary } from "./dictionary";

/**
 * The picture above the crime story, which is the one place in this product
 * where a photograph does work rather than filling space (design spec §10).
 *
 * The front page's cards came later and are the exception §10 argues for;
 * this is what section 10 was written for. The participant is about to go
 * into a database, and the picture is what sets the scene before they do —
 * atmosphere is part of the task here, not decoration.
 *
 * **Rendered on the server**, by `page.tsx`, and handed down as a finished
 * node — the same decision the story itself and the print copy beside it
 * already make. `Workspace` and `SidePanel` are Client Components, so
 * importing this from either would put `DrawnCover`'s geometry and this
 * module's own imports into the client graph of the one screen whose
 * time-to-interactive matters most in the product. Nothing here is
 * interactive, so nothing is lost by keeping it on this side of the wire.
 *
 * **Not in the print copy.** `PrintView` renders the story alone: a
 * full-bleed photograph across the top of a printed sheet spends a page of
 * somebody's ink on atmosphere and pushes the text they wanted onto the
 * second sheet. This component is mounted only inside the on-screen story
 * tab, which `Workspace` already hides under `@media print`.
 */

/**
 * The rendition this surface asks for, and the box it reserves for it.
 *
 * 1600 × 900 is the larger of the two the server stores: a card four hundred
 * pixels wide takes the 800 one, and this is shown at something like its own
 * size. The numbers are written onto the element as well as into the
 * address, because a browser that knows the aspect before the bytes arrive
 * does not move the story out from under somebody who is already reading it
 * — the one thing this screen may never do, since it is under a timer.
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
  /** The hash of the uploaded picture, or empty for a contest that wears a drawn cover. */
  coverHash: string;
  /** Who made the uploaded picture. Empty for a drawn cover, which has nobody to credit. */
  coverAttribution: string;
  dict: PlayDictionary;
}) {
  const t = dict.participant.play.workspace.story;

  return (
    <div className="mb-5">
      <div className="relative aspect-video w-full overflow-hidden rounded-frame">
        {coverHash ? (
          /* Not next/image, for the same reason the front page's card is not:
             these bytes come from the API behind the proxy the rest of this
             app talks to, already cropped, resized and re-encoded by the
             server that stored them, and cached for a year at an address
             carrying their hash. An optimiser has nothing left to do here
             except put a second cache in front of it. */
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
          /* §10.3: a contest with no photograph is not a hole above its
             story. The drawn cover is of the same family — the same 16:9,
             the same scrim, the same title in the same place — and it is
             derived from the contest's own identifier, so an olympiad looks
             like itself every time somebody comes back to it. */
          <DrawnCover seed={contestId} />
        )}

        {/* §10.2. The title does not sit on the photograph but on a scrim
            that resolves to the page's own ground, so its contrast does not
            depend on what happens to be in the bottom third of somebody
            else's picture. The organiser picks the subject; the system owes
            the title its legibility whatever they picked. */}
        <div
          aria-hidden
          className="absolute inset-0 bg-linear-to-t from-scrim-a from-0% via-scrim-b via-38% to-transparent to-76%"
        />

        <h2 className="absolute inset-x-0 bottom-0 px-4 pb-3 text-h3 text-ink">{title}</h2>
      </div>

      {/* Somebody else's work, credited. §10.1 makes the line part of the
          publish gate, so an uploaded picture always has one; a drawn cover
          has none to carry, because its author is us. */}
      {coverHash && coverAttribution ? <p className="mt-2 text-small text-ink-3">{coverAttribution}</p> : null}
    </div>
  );
}
