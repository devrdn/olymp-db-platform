import { Band } from "@/components/layout/band";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

/**
 * Three sentences about what happens when somebody answers a question here.
 *
 * Sentences and rules, with no icons and no cards: an icon beside "a query is
 * written against a live database" says nothing the sentence does not, and
 * three boxes would be the one shape this product does not draw. The division
 * is a hairline, which is what separates everything else on the page.
 *
 * The rule turns with the layout. Side by side the three columns are divided
 * vertically; stacked below the layout's one breakpoint, a left-hand rule
 * would run down the outside of a column and divide nothing, so it becomes a
 * rule above each point instead.
 */
export function HowItWorks({ dict }: { dict: Dictionary }) {
  const t = dict.home.how;
  const points = [
    ["live", t.live],
    ["checked", t.checked],
    ["own", t.own],
  ] as const;

  return (
    <Band className="gap-7">
      <h2 className="text-h3 text-ink">{t.heading}</h2>

      <ul className="grid grid-cols-3 gap-x-10 max-narrow:grid-cols-1 max-narrow:gap-y-6">
        {points.map(([key, point], index) => (
          <li
            key={key}
            className={cn(
              "text-body text-ink-2",
              index > 0 &&
                "border-l border-line pl-10 max-narrow:border-l-0 max-narrow:border-t max-narrow:pt-6 max-narrow:pl-0",
            )}
          >
            {point}
          </li>
        ))}
      </ul>
    </Band>
  );
}
