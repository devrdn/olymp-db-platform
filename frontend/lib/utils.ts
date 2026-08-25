import { clsx, type ClassValue } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";

/**
 * The type scale, told to tailwind-merge.
 *
 * `text-*` is two utilities wearing one prefix: a size and a colour. Merge
 * decides which by looking the name up in Tailwind's stock scale, so a step
 * this project invented — `text-label` — falls through to "colour", collides
 * with the `text-ink-3` beside it and is silently dropped. The label then
 * renders at the inherited 15.5px instead of 11.5px, in the right family and
 * the wrong size, which is exactly the kind of defect that survives review.
 *
 * Clearing the stock scale in `@theme` is what makes this list complete rather
 * than additive: these twelve are every size the system has.
 */
const twMerge = extendTailwindMerge({
  extend: {
    classGroups: {
      "font-size": [
        {
          text: [
            "display",
            "h2",
            "h3",
            "lede",
            "body",
            "small",
            "row",
            "control",
            "control-sm",
            "label",
            "data",
            "narrative",
          ],
        },
      ],
    },
  },
});

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
