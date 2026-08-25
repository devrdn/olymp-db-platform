import * as React from "react";
import { Input as InputPrimitive } from "@base-ui/react/input";

import { cn } from "@/lib/utils";

/**
 * A square field.
 *
 * The registry ships `rounded-lg`, and spec section 5 puts rounding on the
 * outer frame and on small controls only — a field is neither. Square is also
 * what the direction is: this is a register, and a register's cells have
 * corners.
 *
 * The border is `--edge` rather than `--line-2`, because a field is an
 * interactive control and WCAG 1.4.11 holds its boundary to 3:1 while asking
 * nothing of a decorative rule. Focus darkens that border to ink; the ring
 * itself is global (spec section 14), so it is not repeated here.
 */
function Input({ className, type, ...props }: React.ComponentProps<"input">) {
  return (
    <InputPrimitive
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
