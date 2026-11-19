import * as React from "react";
import { Checkbox as CheckboxPrimitive } from "@base-ui/react/checkbox";
import { Check, Minus } from "lucide-react";

import { cn } from "@/lib/utils";

/**
 * A square box with the `--edge` border (3:1), like the field. The caller's
 * `indeterminate` picks mark or dash. The indicator stays mounted and toggles
 * `invisible`, so the mark appears the instant `checked` does instead of
 * waiting on the primitive's transition. On a coarse pointer a transparent
 * pseudo-element widens the press to 44px without moving the box.
 */
function Checkbox({
  className,
  checked,
  indeterminate = false,
  ...props
}: React.ComponentProps<typeof CheckboxPrimitive.Root> & { indeterminate?: boolean }) {
  return (
    <CheckboxPrimitive.Root
      data-slot="checkbox"
      checked={checked}
      indeterminate={indeterminate}
      className={cn(
        "relative flex size-4 shrink-0 items-center justify-center rounded-none border border-edge bg-bg",
        "pointer-coarse:after:absolute pointer-coarse:after:-inset-4",
        "transition-colors duration-(--t-input) ease-standard",
        "hover:border-ink-2 focus-visible:border-ink",
        (checked || indeterminate) && "border-cta bg-cta",
        "disabled:cursor-not-allowed disabled:opacity-45",
        className,
      )}
      {...props}
    >
      <CheckboxPrimitive.Indicator
        keepMounted
        className={cn("flex text-cta-fg", !(checked || indeterminate) && "invisible")}
      >
        {indeterminate ? <Minus className="size-3" /> : <Check className="size-3" />}
      </CheckboxPrimitive.Indicator>
    </CheckboxPrimitive.Root>
  );
}

export { Checkbox };
