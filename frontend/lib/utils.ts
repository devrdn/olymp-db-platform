import { clsx, type ClassValue } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";

/**
 * The project's type scale, told to tailwind-merge. `text-*` is both a size
 * and a colour, and merge reads an unknown step such as `text-label` as a
 * colour, silently dropping it beside `text-ink-3`. The stock scale is cleared
 * in `@theme`, so this list is every size there is.
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
