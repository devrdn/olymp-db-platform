import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

/**
 * The button, on the project's tokens. A plain `<button>` rather than Base UI's
 * `Button`, which saved about 25 KiB gzipped on the play route for identical
 * native behaviour.
 *
 * The focus ring is global (globals.css), sizes come from `--control-h` so one
 * variant serves dense and roomy layouts, and there are no `dark:` utilities
 * since the theme is a variable swap.
 */
const buttonVariants = cva(
  [
    "inline-flex shrink-0 items-center justify-center gap-2 rounded-full whitespace-nowrap",
    "transition-[background-color,border-color,color,opacity] duration-(--t-input) ease-standard",
    "outline-none select-none",
    "disabled:pointer-events-none disabled:opacity-45",
    "[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
  ].join(" "),
  {
    variants: {
      variant: {
        /* The only weight above 500 in the system. */
        primary: "bg-cta font-semibold text-cta-fg hover:opacity-88",
        /* `--edge` is the token held to 3:1 (WCAG 1.4.11), not the decorative hairline. */
        secondary: "border border-edge bg-transparent text-ink hover:border-ink",
        quiet: "border border-transparent text-ink-2 hover:bg-sunk hover:text-ink",
        danger: "bg-bad-wash text-bad hover:bg-bad hover:text-bg",
      },
      size: {
        sm: "h-7 px-3 text-control-sm",
        md: "h-(--control-h) px-4 text-control",
        lg: "h-9.5 px-5 text-control",
        icon: "size-(--control-h) px-0",
      },
    },
    defaultVariants: { variant: "primary", size: "md" },
  },
);

function Button({
  className,
  variant,
  size,
  ...props
}: React.ComponentProps<"button"> & VariantProps<typeof buttonVariants>) {
  return (
    <button
      data-slot="button"
      className={cn(buttonVariants({ variant, size, className }))}
      {...props}
    />
  );
}

export { Button, buttonVariants };
