import Link from "next/link";

import { buttonVariants } from "@/components/ui/button";
import type { ContestSummary } from "@/lib/api/contests";
import { formatMoment } from "@/lib/format/datetime";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";

/**
 * The contest listing, as a register rather than a wall of cards.
 *
 * A row carries more than a tile at the same height and can be compared down a
 * column, which a grid of tiles cannot. It is a real `<table>`: the data is
 * tabular, so the semantics come for free and a screen reader announces which
 * column a cell belongs to.
 *
 * Every string arrives in `dict`. The component holds no copy of its own, so a
 * fourth language needs a dictionary file and nothing here.
 */

const HEAD_CELL =
  "px-3 py-2 font-mono text-[0.625rem] font-medium tracking-[0.11em] text-ink-3 uppercase";

type RegisterProps = {
  contests: ContestSummary[];
  total: number;
  dict: Dictionary;
  locale: Locale;
  filtered?: boolean;
  resetHref?: string;
};

/**
 * "Nothing here" and "nothing matched" are different states and get different
 * screens (spec section 7). The first has no filter to clear, so offering the
 * control would be a lie; the second is useless without it.
 */
function EmptyRegister({
  dict,
  filtered,
  resetHref,
}: Pick<RegisterProps, "dict" | "filtered" | "resetHref">) {
  const copy = filtered ? dict.contests.emptyFiltered : dict.contests.empty;

  return (
    <div className="flex flex-col items-start gap-2 border-t border-line px-1 py-12">
      <p className="font-medium">{copy.title}</p>
      <p className="max-w-[48ch] text-sm text-ink-2">{copy.body}</p>
      {filtered && resetHref ? (
        <Link href={resetHref} className={`${buttonVariants({ variant: "outline" })} mt-2`}>
          {dict.contests.emptyFiltered.reset}
        </Link>
      ) : null}
    </div>
  );
}

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
    <section aria-labelledby="register-heading">
      <div className="flex items-baseline justify-between gap-4 px-1 pb-3">
        <h2 id="register-heading" className="text-lg font-medium tracking-tight">
          {t.heading}
        </h2>
        <span className="font-mono text-xs text-ink-3 tabular-nums">
          {total} {t.countLabel}
        </span>
      </div>

      {contests.length === 0 ? (
        <EmptyRegister dict={dict} filtered={filtered} resetHref={resetHref} />
      ) : (
        /* Wide content scrolls inside its own container, so the page body never
           scrolls sideways on a narrow screen. */
        <div className="overflow-x-auto border-t border-line">
          <table className="w-full min-w-[46rem] border-collapse text-left">
            <thead>
              <tr className="border-b border-line-2">
                <th scope="col" className={`${HEAD_CELL} w-10 text-right`}>
                  {t.columns.index}
                </th>
                <th scope="col" className={HEAD_CELL}>
                  {t.columns.contest}
                </th>
                <th scope="col" className={HEAD_CELL}>
                  {t.columns.state}
                </th>
                <th scope="col" className={HEAD_CELL}>
                  {t.columns.enrollment}
                </th>
                <th scope="col" className={HEAD_CELL}>
                  {t.columns.starts}
                </th>
              </tr>
            </thead>
            <tbody>
              {contests.map((contest, index) => (
                <tr key={contest.id} className="border-b border-line align-baseline">
                  <td className="px-3 py-3 text-right font-mono text-[0.6875rem] text-ink-3 tabular-nums">
                    {String(index + 1).padStart(2, "0")}
                  </td>
                  <td className="px-3 py-3">
                    <span className="block font-medium tracking-tight">{contest.title}</span>
                    {contest.description ? (
                      <span className="mt-1 block max-w-[52ch] text-sm text-ink-2">
                        {contest.description}
                      </span>
                    ) : null}
                  </td>
                  <td className="px-3 py-3 text-sm">{t.status[contest.status]}</td>
                  <td className="px-3 py-3 text-sm">{t.enrollment[contest.enrollment]}</td>
                  <td className="px-3 py-3 font-mono text-[0.6875rem] text-ink-2 tabular-nums">
                    {contest.startsAt
                      ? formatMoment(contest.startsAt, { locale })
                      : t.unscheduled}
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
