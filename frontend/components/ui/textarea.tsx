import * as React from "react";

import { cn } from "@/lib/utils";

/**
 * A field for prose: the same square `--edge` border as `Input`, set in the
 * narrative face the story is read in. `field-sizing-content` grows it within
 * `min-h`/`max-h`.
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
