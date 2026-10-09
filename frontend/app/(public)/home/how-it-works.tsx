import { Band } from "@/components/layout/band";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

/**
 * Three sentences on how answering works, divided by hairlines, no icons or
 * cards. Side by side the rules are vertical; stacked they move above each
 * point.
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

      {/* Three columns only from `wide` (1024px): at tablet width each point
         broke across four lines. */}
      <ul className="grid grid-cols-3 gap-x-10 max-wide:grid-cols-1 max-wide:gap-y-6">
        {points.map(([key, point], index) => (
          <li
            key={key}
            className={cn(
              "text-body text-ink-2",
              index > 0 &&
                "border-l border-line pl-10 max-wide:border-l-0 max-wide:border-t max-wide:pt-6 max-wide:pl-0",
            )}
          >
            {point}
          </li>
        ))}
      </ul>
    </Band>
  );
}
