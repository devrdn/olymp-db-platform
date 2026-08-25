import * as React from "react";

import { cn } from "@/lib/utils";

/**
 * The field label is the tab on a card-index divider: mono, uppercase, small
 * and wide-tracked (the `label` step of spec section 4).
 *
 * That is the same treatment column headings and pane headings get, which is
 * the point — one voice names things throughout the product, and it is never
 * the voice that says them.
 */
function Label({ className, ...props }: React.ComponentProps<"label">) {
  return (
    <label
      data-slot="label"
      className={cn(
        "font-mono text-label text-ink-3 uppercase select-none",
        "peer-disabled:opacity-50",
        className,
      )}
      {...props}
    />
  );
}

export { Label };
