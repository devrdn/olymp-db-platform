import * as React from "react";

import { cn } from "@/lib/utils";

/**
 * A field for prose.
 *
 * The same square edge and the same `--edge` border as `Input`, because they
 * are the same control at different heights and a rounded one beside a square
 * one is the small inconsistency that makes a form look assembled from parts.
 *
 * Set in the narrative face rather than the interface one. What is typed here
 * is the crime story, and it is read back on the participant's screen in that
 * face; an author writing in one typeface and publishing in another is judging
 * a paragraph's rhythm against the wrong measure.
 *
 * `field-sizing-content` lets it grow with what is in it, bounded by
 * `min-h`/`max-h` — a scroll bar inside a box inside a page is where a long
 * story goes to be un-editable.
 */
function Textarea({ className, ...props }: React.ComponentProps<"textarea">) {
  return (
    <textarea
      data-slot="textarea"
      className={cn(
        "field-sizing-content min-h-40 w-full rounded-none border border-edge bg-transparent px-3 py-2.5",
        "font-serif text-narrative text-ink placeholder:text-ink-3",
        "transition-colors duration-(--t-input) ease-standard",
        "hover:border-ink-2 focus-visible:border-ink",
        "disabled:cursor-not-allowed disabled:bg-sunk disabled:text-ink-3",
        "aria-invalid:border-bad",
        className,
      )}
      {...props}
    />
  );
}

export { Textarea };
