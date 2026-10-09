import * as React from "react";

import { cn } from "@/lib/utils";

/**
 * The field label: mono, uppercase, small and wide-tracked, the same voice as
 * column and pane headings.
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
