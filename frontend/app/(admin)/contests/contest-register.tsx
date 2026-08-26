import Link from "next/link";

import { Tag } from "@/components/ui/tag";
import { ContestWindow } from "@/components/product/contest-window";
import { StateView } from "@/components/product/state-view";
import { buttonVariants } from "@/components/ui/button";
import type { ContestStatus, ContestSummary } from "@/lib/api/contests";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

/**
 * The contest listing, as a register rather than a wall of cards.
 *
 * A row carries more than a tile at the same height and can be compared down a
 * column, which a grid of tiles cannot. It is a real `<table>`: the data is
 * tabular, so the semantics come for free and a screen reader announces which
 * column a cell belongs to.
 *
 * On a narrow screen it stays a table. Restacking into one card per contest is
 * the usual answer and it is the wrong one here: it dissolves the columns, and
 * comparing down a column is the entire reason this is a register and not the
 * wall of cards the direction was chosen to get away from.
 *
 * What gives instead is the column count. Enrollment and format fold under the
 * title, where they read as a caption on the contest rather than as columns
 * too thin to compare; state and the window stay, because those are what a
 * reader scans a register for. Nothing is ever shown twice: the folded line
 * only exists at the width where its columns are gone. Past that the box —
 * and only this box — scrolls sideways.
 *
 * Every string arrives in `dict`. The component holds no copy of its own, so a
 * fourth language needs a dictionary file and nothing here.
 */

/** One tone per state, and the accent spent only on what is happening now. */
const STATUS_TONE: Record<ContestStatus, "live" | "good" | "mute"> = {
  draft: "mute",
  published: "good",
  running: "live",
  finished: "mute",
  archived: "mute",
};

/* Padding comes from the density tokens, so the same register is comfortable
   in the constructor and compact in the query log without a second component
   or a prop threaded through four layers. */
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
          {/* The register's one action. A link rather than a button: it
              navigates, and a button that navigates loses the middle click,
              the new tab and the address the browser would otherwise show. */}
          <Link href="/contests/new" className={buttonVariants({ size: "sm" })}>
            {dict.workspace.create.action}
          </Link>
        </div>
      </div>

      {contests.length === 0 ? (
        <div className="border-t border-line">
          <StateView
            state={
              /* "Nothing here" and "nothing matched" are different states and
                 get different screens. The first has no filter
                 to clear, so offering the control would be a lie; the second is
                 a dead end without it. The type refuses to mix them up. */
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
                  {/* The register line number: a position in an ordered list,
                      which is what the number on a card in a drawer is. */}
                  <td className={cn(CELL, "pr-0 text-right font-mono text-data text-ink-3")}>
                    {String(index + 1).padStart(2, "0")}
                  </td>
                  <td className={CELL}>
                    {/* The title is the way in. The whole row is not: a row
                        that navigates cannot hold a second control, and this
                        one will hold state changes before long. */}
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
                    <Tag tone={STATUS_TONE[contest.status]}>{t.status[contest.status]}</Tag>
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
