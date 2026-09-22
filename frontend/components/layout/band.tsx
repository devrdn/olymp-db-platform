import { cn } from "@/lib/utils";

/**
 * The hatched band.
 *
 * A page is a stack of bands. Each one is three grid tracks: a hatched field,
 * the 1136px content column, another hatched field — and it is those fields,
 * not a border, that hold the column in place. Nothing on the page is boxed;
 * the structure comes from ruled margins, which is what a register sheet has
 * instead of cards.
 *
 * Below the one breakpoint the direction has, the fields are removed and the
 * column takes the full width (the mobile reset). The template
 * drops to a single track rather than keeping three at zero width: a hidden
 * grid item leaves the flow entirely, so with three tracks the content would
 * slide into the first one and render 104px wide inside a 375px screen.
 */
export function Band({
  className,
  fill,
  rule = true,
  children,
  ...props
}: React.ComponentProps<"section"> & {
  /** Take the remaining height, so the hatched fields run to the fold. */
  fill?: boolean;
  /**
   * Whether this band draws the rule that separates it from the next one.
   * The last band on a page has nothing after it, and a hairline along the
   * bottom of the page separates the page from the browser.
   *
   * A prop rather than a class, because `className` lands on the content
   * column and the rule is drawn by the section around it: passing
   * `border-b-0` looks like it should work, does nothing, and takes a
   * browser to notice — which is exactly how the footer shipped with a rule
   * under the last thing on the page.
   */
  rule?: boolean;
}) {
  return (
    <section
      className={cn(
        "grid grid-cols-[minmax(var(--gutter-min),1fr)_minmax(0,var(--container-column))_minmax(var(--gutter-min),1fr)] max-narrow:grid-cols-[minmax(0,1fr)]",
        rule && "border-b border-line",
        fill && "flex-1",
      )}
      {...props}
    >
      <div aria-hidden className="hatched border-x border-line max-narrow:hidden" />
      <div
        className={cn(
          "flex min-w-0 flex-col px-10 py-14 max-narrow:px-4.5 max-narrow:py-9",
          className,
        )}
      >
        {children}
      </div>
      <div aria-hidden className="hatched border-x border-line max-narrow:hidden" />
    </section>
  );
}
