import Link from "next/link";

import { ContestWindow } from "@/components/product/contest-window";
import { buttonVariants } from "@/components/ui/button";
import { StateView } from "@/components/product/state-view";
import { ScrollBox } from "@/components/ui/scroll-box";
import { Tag } from "@/components/ui/tag";
import type { ContestSummary } from "@/lib/api/contests";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { fill } from "@/lib/i18n/fill";
import { cn } from "@/lib/utils";

import { EnrollButton } from "./enroll-button";
import { CONTEST_STATUS_TONE, hasEnded } from "@/lib/api/contests-terms";

/**
 * A participant's register, shared by `/my` and `/open`: the rows are the
 * same, only the contests and the heading differ, so one component keeps a
 * fix from reaching only one screen. Rows rather than tiles, so columns
 * compare; the columns are the window and the way in. No thumbnails: the spec
 * allows a photograph only above the crime story.
 *
 * Below `wide` the row number goes and the way in takes only the room it
 * needs, so the window keeps its column; on a phone the state and the window
 * fold into the caption under the title, leaving it beside the way in
 * (SPEC.md §5).
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
 * Whether there is a waiting room to sit in: enrolled in a contest that has
 * not started. The play route turns it into the console at the start, so
 * nobody has to reload this list to find out.
 */
function canWait(contest: ContestSummary): boolean {
  return contest.enrolled && contest.status === "published";
}

/**
 * Whether the contest has a standings table worth opening: from the moment
 * it runs, and after. Public, so offered whether enrolled or not.
 */
function hasTable(contest: ContestSummary): boolean {
  return contest.status === "running" || hasEnded(contest.status);
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
        <ScrollBox>
          <table className="w-full border-collapse text-left max-narrow:[--row-px:--spacing(2)] wide:min-w-3xl">
            <thead>
              <tr>
                <th scope="col" className={cn(HEAD, "w-10 pr-0 text-right max-wide:hidden")}>
                  {shared.columns.index}
                </th>
                <th scope="col" className={HEAD}>
                  {shared.columns.contest}
                </th>
                <th scope="col" className={cn(HEAD, "w-36 max-narrow:hidden")}>
                  {shared.columns.state}
                </th>
                <th scope="col" className={cn(HEAD, "w-52 max-narrow:hidden")}>
                  {shared.columns.starts}
                </th>
                <th scope="col" className={cn(HEAD, "w-36 max-wide:w-px")}>
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
                  <td className={cn(CELL, "pr-0 text-right font-mono text-data text-ink-3 max-wide:hidden")}>
                    {String(index + 1).padStart(2, "0")}
                  </td>
                  <td className={CELL}>
                    <span className="block text-row text-ink">{contest.title}</span>
                    {contest.description ? (
                      <span className="mt-1.5 block max-w-body text-small text-ink-2">
                        {contest.description}
                      </span>
                    ) : null}
                    <span className="mt-2 hidden max-narrow:block">
                      <Tag tone={CONTEST_STATUS_TONE[contest.status]}>{shared.status[contest.status]}</Tag>
                    </span>
                    <span data-caption className="mt-2 hidden font-mono text-data text-ink-2 max-narrow:block">
                      <ContestWindow
                        startsAt={contest.startsAt}
                        endsAt={contest.endsAt}
                        locale={locale}
                        unscheduled={shared.unscheduled}
                        until={shared.until}
                      />
                    </span>
                    <span data-caption className="mt-2 block font-mono text-data text-ink-3">
                      {shared.mode[contest.questionMode]}
                    </span>
                  </td>
                  <td className={cn(CELL, "max-narrow:hidden")}>
                    <Tag tone={CONTEST_STATUS_TONE[contest.status]}>{shared.status[contest.status]}</Tag>
                  </td>
                  <td className={cn(CELL, "font-mono text-data whitespace-nowrap text-ink-2 max-narrow:hidden")}>
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
                        <EnrollButton
                          contestId={contest.id}
                          dict={{ participant: dict.participant, errors: dict.errors }}
                        />
                      ) : canOpen(contest) ? (
                        /* The one row with something to do now; it outranks "you are
                           enrolled", which is a state, not a step. */
                        <Link
                          href={`/contests/${contest.id}/play`}
                          className={cn(buttonVariants({ variant: "primary", size: "sm" }))}
                        >
                          {t.openConsole}
                        </Link>
                      ) : canWait(contest) ? (
                        <Link
                          href={`/contests/${contest.id}/play`}
                          className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}
                        >
                          {t.waitingRoom}
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
                          aria-label={fill(dict.leaderboard.openLabel, { title: contest.title })}
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
        </ScrollBox>
      )}
    </section>
  );
}
