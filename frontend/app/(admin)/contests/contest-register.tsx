import Link from "next/link";

import { Tag } from "@/components/ui/tag";
import { ContestWindow } from "@/components/product/contest-window";
import { StateView } from "@/components/product/state-view";
import { buttonVariants } from "@/components/ui/button";
import type { ContestSummary } from "@/lib/api/contests";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";
import { CONTEST_STATUS_TONE } from "@/lib/api/contests-terms";

/**
 * The contest listing as a real `<table>`, so rows compare down columns and
 * screen readers announce the column. On a narrow screen it stays a table:
 * enrollment and format fold under the title (shown only at that width), and
 * past that only this box scrolls sideways.
 */

/* Padding comes from the density tokens, so one register serves comfortable and compact layouts. */
const HEAD =
  "border-b border-line-2 px-(--row-px) py-2.5 font-mono text-label font-medium text-ink-3 uppercase";
const CELL = "border-b border-line px-(--row-px) py-(--row-py) align-baseline";

type RegisterProps = {
  contests: ContestSummary[];
  total: number;
  dict: Dictionary;
  locale: Locale;
  filtered?: boolean;
  resetHref?: string;
};

export function ContestRegister({
  contests,
  total,
  dict,
  locale,
  filtered,
  resetHref,
}: RegisterProps) {
  const t = dict.contests;

  return (
    <section aria-labelledby="register-heading" className="flex flex-col">
      <div className="flex flex-wrap items-baseline justify-between gap-x-6 gap-y-2 pb-6">
        <h1 id="register-heading" className="text-h2 text-ink">
          {t.heading}
        </h1>
        <div className="flex items-center gap-5">
          <span className="font-mono text-data text-ink-3">
            {total} {t.countLabel}
          </span>
          {/* A link, not a button: it navigates, and keeps middle click and new tab. */}
          <Link href="/contests/new" className={buttonVariants({ size: "sm" })}>
            {dict.workspace.create.action}
          </Link>
        </div>
      </div>

      {contests.length === 0 ? (
        <div className="border-t border-line">
          <StateView
            state={
              /*
               * "Nothing here" has no filter to reset; "nothing matched" needs
               * one. The type keeps them apart.
               */
              filtered && resetHref
                ? {
                    kind: "empty-filtered",
                    title: t.emptyFiltered.title,
                    body: t.emptyFiltered.body,
                    reset: { label: t.emptyFiltered.reset, href: resetHref },
                  }
                : { kind: "empty", title: t.empty.title, body: t.empty.body }
            }
          />
        </div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full min-w-lg border-collapse text-left narrow:min-w-3xl">
            <thead>
              <tr>
                <th scope="col" className={cn(HEAD, "w-10 pr-0 text-right")}>
                  {t.columns.index}
                </th>
                <th scope="col" className={HEAD}>
                  {t.columns.contest}
                </th>
                <th scope="col" className={cn(HEAD, "w-36")}>
                  {t.columns.state}
                </th>
                <th scope="col" className={cn(HEAD, "w-32 max-narrow:hidden")}>
                  {t.columns.enrollment}
                </th>
                <th scope="col" className={cn(HEAD, "w-40 max-narrow:hidden")}>
                  {t.columns.mode}
                </th>
                <th scope="col" className={cn(HEAD, "w-52")}>
                  {t.columns.starts}
                </th>
              </tr>
            </thead>
            <tbody>
              {contests.map((contest, index) => (
                <tr
                  key={contest.id}
                  className="transition-colors duration-(--t-input) ease-standard hover:bg-panel"
                >
                  {/* The row's position in the list. */}
                  <td className={cn(CELL, "pr-0 text-right font-mono text-data text-ink-3")}>
                    {String(index + 1).padStart(2, "0")}
                  </td>
                  <td className={CELL}>
                    {/* Only the title links: a navigating row could not hold other controls. */}
                    <Link
                      href={`/contests/${contest.id}`}
                      className="block w-fit text-row text-ink underline decoration-edge underline-offset-4 transition-colors duration-(--t-input) ease-standard hover:decoration-ink"
                    >
                      {contest.title || t.untitled}
                    </Link>
                    {contest.description ? (
                      <span className="mt-1.5 block max-w-body text-small text-ink-2">
                        {contest.description}
                      </span>
                    ) : null}
                    <span className="mt-2 hidden font-mono text-data text-ink-3 max-narrow:block">
                      {t.enrollment[contest.enrollment]} · {t.mode[contest.questionMode]}
                    </span>
                  </td>
                  <td className={CELL}>
                    <Tag tone={CONTEST_STATUS_TONE[contest.status]}>{t.status[contest.status]}</Tag>
                  </td>
                  <td className={cn(CELL, "text-small whitespace-nowrap text-ink-2 max-narrow:hidden")}>
                    {t.enrollment[contest.enrollment]}
                  </td>
                  <td className={cn(CELL, "font-mono text-data whitespace-nowrap text-ink-3 max-narrow:hidden")}>
                    {t.mode[contest.questionMode]}
                  </td>
                  <td className={cn(CELL, "font-mono text-data whitespace-nowrap text-ink-2")}>
                    <ContestWindow
                      startsAt={contest.startsAt}
                      endsAt={contest.endsAt}
                      locale={locale}
                      unscheduled={t.unscheduled}
                      until={t.until}
                    />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
