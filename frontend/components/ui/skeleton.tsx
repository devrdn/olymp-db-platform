import { cn } from "@/lib/utils";

/**
 * A placeholder shaped like the thing that is coming, not a spinner.
 *
 * A spinner says "wait"; a skeleton says "here is what will be here", and it
 * says it in the real layout, so nothing jumps when the data lands. That is
 * why there is no `<StateView kind="loading">`: the loading state is the only
 * one of the nine whose appearance is a property of the content, so each
 * container draws its own out of these (spec section 7).
 *
 * The shimmer is one of the two perpetual animations the system allows: it
 * reports that the wait is still live rather than hung.
 */
export function Skeleton({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      aria-hidden
      className={cn("relative overflow-hidden rounded-full bg-line-2", className)}
      {...props}
    >
      <span className="absolute inset-0 bg-linear-to-r from-transparent via-bg to-transparent motion-safe:animate-shimmer" />
    </div>
  );
}
