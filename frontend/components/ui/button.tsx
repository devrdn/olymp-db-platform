import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

/**
 * The button, drawn from this project's tokens rather than from the registry's.
 *
 * A plain `<button>` with a class, not a Base UI primitive (finding 6). What
 * that primitive contributed here was `useRender`/`mergeProps` and a
 * `render` prop this codebase never once used — measured, it cost 21.5 KiB
 * raw of `button.tsx`-plus-Base-UI per chunk that pulled it, and the play
 * route pulled two byte-identical copies of exactly that (one through
 * `workspace.tsx`, one through `app/error.tsx` → `StateView`) on top of
 * 10.1 KiB of shared Base UI runtime: about 53 KiB raw, 25 KiB gzipped, of a
 * 159/56 KiB route, spent on a `<button>` and an `<input>`. This is the same
 * trade `components/ui/tabs.tsx` recorded when it stopped using Base UI's
 * `Tabs`, and it is the same screen paying for it.
 *
 * Nothing about the element changed: Base UI's `Button`, given
 * `nativeButton` (its default) and no `render`, renders a native `<button>`
 * and forwards `disabled` to it natively. Focus, the disabled state and
 * every event handler are the browser's, exactly as they were.
 *
 * Three decisions separate it from what `shadcn add button` produces, and each
 * of them is a rule from the spec rather than a preference:
 *
 * - The focus treatment is a 2px accent ring at 2px offset, applied globally in
 *   globals.css. The registry's `ring-3 ring-ring/50` is a
 *   glow, and section 15 has no glows in it.
 * - Sizes come from `--control-h`, so the same button is 34px in a profile and
 *   28px in a results grid without a second variant.
 * - There is no `dark:` utility anywhere. The theme is a variable swap on
 *   `data-theme`, so a colour that is written once is already correct in both
 *   themes; a `dark:` class here would be a second, silently diverging source.
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
        /* The one dark mass on a white page, and the only place in the system
           whose weight goes above 500. */
        primary: "bg-cta font-semibold text-cta-fg hover:opacity-88",
        /* Outlined in the control edge, which is the token WCAG 1.4.11 holds
           to 3:1 — not the decorative hairline. */
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
