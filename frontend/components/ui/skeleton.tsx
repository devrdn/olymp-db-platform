import { cn } from "@/lib/utils";

/**
 * A placeholder in the real layout, so nothing jumps when data lands. Each
 * container draws its own loading state from these, which is why `StateView`
 * has no `loading`. The shimmer shows the wait is live.
 *
 * A width on a skeleton is a floor for the flex column holding it, even with
 * `max-w-full`, so a growing column of them carries `min-w-0`.
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
