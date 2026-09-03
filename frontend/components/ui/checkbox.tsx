import * as React from "react";
import { Checkbox as CheckboxPrimitive } from "@base-ui/react/checkbox";
import { Check, Minus } from "lucide-react";

import { cn } from "@/lib/utils";

/**
 * A square box, to match the register: this is a listing of rows, not a
 * rounded form. Its border is `--edge`, the same 3:1 boundary the field uses,
 * so a checkbox reads as one interactive family with the input and the
 * button.
 *
 * The mark and the dash are picked by the caller's own `indeterminate` prop
 * rather than re-derived from the primitive's internal state: a "select the
 * page" checkbox already knows whether it is showing "all" or "some" from the
 * ids it was given, so asking the DOM to tell it back would be redundant.
 * The indicator stays mounted at all times and toggles with `invisible`
 * rather than being mounted/unmounted by the primitive's own open/close
 * transition, so the mark appears the instant `checked` does — nothing here
 * depends on that transition's timing.
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
        "flex size-4 shrink-0 items-center justify-center rounded-none border border-edge bg-bg",
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
