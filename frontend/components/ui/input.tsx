import * as React from "react";

import { cn } from "@/lib/utils";

/**
 * A square field. A plain `<input>`: Base UI's `Input` only integrates with
 * Base UI's `Field`, and this project has its own. The border is `--edge`
 * because WCAG 1.4.11 holds a control's boundary to 3:1; the focus ring is
 * global.
 */
function Input({ className, type, ...props }: React.ComponentProps<"input">) {
  return (
    <input
      type={type}
      data-slot="input"
      className={cn(
        "h-(--control-h) w-full min-w-0 rounded-none border border-edge bg-transparent px-3",
        "text-control text-ink placeholder:text-ink-3",
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

export { Input };
