import Link from "next/link";

import { ContestWindow } from "@/components/product/contest-window";
import { buttonVariants } from "@/components/ui/button";
import { StateView } from "@/components/product/state-view";
import { Tag } from "@/components/ui/tag";
import type { ContestSummary } from "@/lib/api/contests";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { EnrollButton } from "./enroll-button";
import { CONTEST_STATUS_TONE } from "@/lib/api/contests-terms";

/**
 * A participant's register, shared by `/my` and `/open`: the rows are the
 * same, only the contests and the heading differ, so one component keeps a
 * fix from reaching only one screen. Rows rather than tiles, so columns
 * compare; the columns are the window and the way in. No thumbnails: the spec
 * allows a photograph only above the crime story.
 */

const HEAD =
  "border-b border-line-2 px-(--row-px) py-2.5 font-mono text-label font-medium text-ink-3 uppercase";
const CELL = "border-b border-line px-(--row-px) py-(--row-py) align-baseline";

/**
 * Whether joining is offered: only for an open, published contest the
 * participant is not yet in. On a catalogue listing both kinds, offering it
 * to an enrolled participant would turn an ordinary state into an
 * `already_enrolled` error.
 */
function canOfferToJoin(contest: ContestSummary): boolean {
  return !contest.enrolled && contest.enrollment === "open" && contest.status === "published";
}

/**
 * Whether there is a contest to walk into: enrolled and running. A published
 * contest has not started, and a finished one has no console.
 */
function canOpen(contest: ContestSummary): boolean {
  return contest.enrolled && contest.status === "running";
}

/**
 * Whether the contest has a standings table worth opening: from the moment
 * it runs, and after. Public, so offered whether enrolled or not.
 */
function hasTable(contest: ContestSummary): boolean {
  return contest.status === "running" || contest.status === "finished" || contest.status === "archived";
}

export function ParticipantRegister({
  contests,
  total,
  dict,
  locale,
  heading,
  countLabel,
  empty,
}: {
  contests: ContestSummary[];
  total: number;
  dict: Dictionary;
  locale: Locale;
  heading: string;
  countLabel: string;
  /**
   * What to say when there is nothing, and where to go. An empty "mine"
   * points to the open list; an empty open list has nowhere to point.
   */
  empty: { title: string; body: string; action?: { label: string; href: string } };
}) {
  const t = dict.participant;
  const shared = dict.contests;

  return (
    <section aria-labelledby="participant-heading" className="flex flex-col">
      <div className="flex flex-wrap items-baseline justify-between gap-x-6 gap-y-2 pb-6">
        <h1 id="participant-heading" className="text-h2 text-ink">
          {heading}
        </h1>
        <span className="font-mono text-data text-ink-3">
          {total} {countLabel}
        </span>
      </div>

      {contests.length === 0 ? (
        <div className="border-t border-line">
          {/* `empty`, never `empty-filtered`: neither screen has filters. */}
          <StateView state={{ kind: "empty", ...empty }} />
        </div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full min-w-lg border-collapse text-left narrow:min-w-3xl">
            <thead>
              <tr>
                <th scope="col" className={cn(HEAD, "w-10 pr-0 text-right")}>
                  {shared.columns.index}
                </th>
                <th scope="col" className={HEAD}>
                  {shared.columns.contest}
                </th>
                <th scope="col" className={cn(HEAD, "w-36")}>
                  {shared.columns.state}
                </th>
                <th scope="col" className={cn(HEAD, "w-52")}>
                  {shared.columns.starts}
                </th>
                <th scope="col" className={cn(HEAD, "w-36")}>
                  {t.columns.action}
                </th>
              </tr>
            </thead>
            <tbody>
              {contests.map((contest, index) => (
                <tr
                  key={contest.id}
                  className="transition-colors duration-(--t-input) ease-standard hover:bg-panel"
                >
                  <td className={cn(CELL, "pr-0 text-right font-mono text-data text-ink-3")}>
                    {String(index + 1).padStart(2, "0")}
                  </td>
                  <td className={CELL}>
                    <span className="block text-row text-ink">{contest.title}</span>
                    {contest.description ? (
                      <span className="mt-1.5 block max-w-body text-small text-ink-2">
                        {contest.description}
                      </span>
                    ) : null}
                    <span className="mt-2 block font-mono text-data text-ink-3">
                      {shared.mode[contest.questionMode]}
                    </span>
                  </td>
                  <td className={CELL}>
                    <Tag tone={CONTEST_STATUS_TONE[contest.status]}>{shared.status[contest.status]}</Tag>
                  </td>
                  <td className={cn(CELL, "font-mono text-data whitespace-nowrap text-ink-2")}>
                    <ContestWindow
                      startsAt={contest.startsAt}
                      endsAt={contest.endsAt}
                      locale={locale}
                      unscheduled={shared.unscheduled}
                      until={shared.until}
                    />
                  </td>
                  <td className={CELL}>
                    <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
                      {canOfferToJoin(contest) ? (
                        <EnrollButton contestId={contest.id} dict={dict} />
                      ) : canOpen(contest) ? (
                        /* The one row with something to do now; it outranks "you are
                           enrolled", which is a state, not a step. */
                        <Link
                          href={`/contests/${contest.id}/play`}
                          className={cn(buttonVariants({ variant: "primary", size: "sm" }))}
                        >
                          {t.openConsole}
                        </Link>
                      ) : contest.enrolled ? (
                        /* Before the invitation note: "by invitation" on a contest one is
                           already in reads as though one were not. */
                        <span className="text-small text-ink-2">{t.enrolled}</span>
                      ) : (
                        /* The state column already says why; a disabled button would only
                           repeat it. */
                        <span className="text-small text-ink-3">
                          {contest.enrollment === "invite_only" ? t.byInvitation : t.noAction}
                        </span>
                      )}
                      {hasTable(contest) ? (
                        <Link
                          href={`/contests/${contest.id}/leaderboard`}
                          aria-label={dict.leaderboard.openLabel.replace("{title}", contest.title)}
                          className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}
                        >
                          {dict.leaderboard.open}
                        </Link>
                      ) : null}
                    </div>
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
