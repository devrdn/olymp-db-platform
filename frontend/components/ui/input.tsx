import * as React from "react";

import { cn } from "@/lib/utils";

/**
 * A square field.
 *
 * A plain `<input>`, not Base UI's (finding 6). Base UI's `Input` is its
 * `Field.Control` under another name — it exists to integrate with Base UI's
 * own `Field`, and this project has its own (`components/ui/field.tsx`), so
 * every one of the states that primitive tracks (filled, dirty, touched)
 * was being computed for nothing while pulling the whole Field machinery
 * into the bundle. See `button.tsx`'s own doc for what that weighed on the
 * one route it matters most on.
 *
 * The registry ships `rounded-lg`, and the specification puts rounding on the
 * outer frame and on small controls only — a field is neither. Square is also
 * what the direction is: this is a register, and a register's cells have
 * corners.
 *
 * The border is `--edge` rather than `--line-2`, because a field is an
 * interactive control and WCAG 1.4.11 holds its boundary to 3:1 while asking
 * nothing of a decorative rule. Focus darkens that border to ink; the ring
 * itself is global, so it is not repeated here.
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
