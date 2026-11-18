import { cn } from "@/lib/utils";

/**
 * The box a wide table scrolls in, so the page never scrolls sideways.
 *
 * It is `relative` on purpose: an absolutely positioned child such as an
 * `sr-only` label is clipped only by a scroller that is its containing block.
 * Otherwise it sits at its static position past the box's edge and widens the
 * whole document. A box that scrolls both ways passes `overflow-auto`.
 */
export function ScrollBox({ className, ...props }: React.ComponentProps<"div">) {
  return <div className={cn("relative overflow-x-auto", className)} {...props} />;
}
