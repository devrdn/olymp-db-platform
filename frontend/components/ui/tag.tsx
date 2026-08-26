import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

/**
 * A state, named in one word.
 *
 * One accent and three semantics: the accent marks
 * what is live and nothing else, and good / warn / bad carry the rest. A tone
 * outside this list is a change to the system, not a decision taken here.
 *
 * `live` earns the only perpetual animation on a listing: a contest that is
 * running right now is a fact that changes while the page is open, and a
 * pulsing dot states it without a second line of text.
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
