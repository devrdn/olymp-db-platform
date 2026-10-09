import { cn } from "@/lib/utils";

/**
 * A page band: a hatched field, the 1136px content column, another hatched
 * field. The fields, not borders, hold the column.
 *
 * Below the breakpoint the template drops to a single track: a hidden grid item
 * leaves the flow, so with three tracks the content would slide into the first
 * one and render 104px wide.
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
   * Draws the rule under the band; off for the last one. A prop rather than a
   * class because `className` lands on the inner column, where `border-b-0`
   * would do nothing.
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
