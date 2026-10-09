import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

/**
 * A state in one word. The accent marks only what is live; good, warn and bad
 * carry the rest. `live` has the only perpetual animation on a listing.
 */
const tagVariants = cva(
  "inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 font-mono text-label uppercase",
  {
    variants: {
      tone: {
        live: "bg-accent-wash text-accent",
        good: "bg-good-wash text-good",
        warn: "bg-warn-wash text-warn",
        bad: "bg-bad-wash text-bad",
        mute: "bg-sunk text-ink-3",
        ink: "bg-cta text-cta-fg",
      },
    },
    defaultVariants: { tone: "mute" },
  },
);

export function Tag({
  tone,
  className,
  children,
  ...props
}: React.ComponentProps<"span"> & VariantProps<typeof tagVariants>) {
  return (
    <span className={cn(tagVariants({ tone, className }))} {...props}>
      {tone === "live" ? (
        <span
          aria-hidden
          className="size-1.5 shrink-0 rounded-full bg-accent motion-safe:animate-live"
        />
      ) : null}
      {children}
    </span>
  );
}
