import Link from "next/link";

import { ContestWindow } from "@/components/product/contest-window";
import { buttonVariants } from "@/components/ui/button";
import { StateView } from "@/components/product/state-view";
import { Tag } from "@/components/ui/tag";
import type { ContestStatus, ContestSummary } from "@/lib/api/contests";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { EnrollButton } from "./enroll-button";

/**
 * A participant's register, used by both of their screens.
 *
 * One component, not two, because the rows are identical: the same columns,
 * the same window, the same rule for what a row offers. What differs is which
 * contests are in it and what the screen is called, so those arrive as props.
 * Two components differing by a heading is how one of them gets a fix and the
 * other does not.
 *
 * A register, like the author's, and for the same reason the direction gives:
 * rows compare down a column and tiles do not. It is not the author's register
 * with a column removed, though. What a student scans for is different — when
 * does it start, can I get in — so the columns are the window and the way in,
 * and the author's audit columns are gone.
 *
 * No thumbnails. The specification allows a photograph above the crime
 * story, where atmosphere is part of the task, and nowhere else; a strip of
 * eight of them here would be the wall of cards this direction was chosen to
 * get away from.
 */

/** One tone per state, and the accent spent only on what is happening now. */
const STATUS_TONE: Record<ContestStatus, "live" | "good" | "mute"> = {
  draft: "mute",
  published: "good",
  running: "live",
  finished: "mute",
  archived: "mute",
};

const HEAD =
  "border-b border-line-2 px-(--row-px) py-2.5 font-mono text-label font-medium text-ink-3 uppercase";
const CELL = "border-b border-line px-(--row-px) py-(--row-py) align-baseline";

/**
 * Whether joining is worth offering on this row.
 *
 * Somebody already registered is never offered it. The listing used to be
 * unable to say — the summary carried no such field — so the button appeared
 * wherever joining was possible and the API answered `already_enrolled`,
 * which was tolerable while "mine" and "open to me" shared one screen. On a
 * catalogue that lists both, it turns an ordinary state into an error message.
 *
 * `draft` and `archived` never appear in a participant's scope. `finished`
 * cannot be joined, and `running` is the game loop's to open, which is step 5.
 */
function canOfferToJoin(contest: ContestSummary): boolean {
  return !contest.enrolled && contest.enrollment === "open" && contest.status === "published";
}

/**
 * Whether there is a contest to walk into.
 *
 * Enrolled and running, and nothing else: a published contest has not started,
 * and a finished one has no console to open. Without this the console existed
 * and nothing in the interface led to it, which is the same as it not
 * existing.
 */
function canOpen(contest: ContestSummary): boolean {
  return contest.enrolled && contest.status === "running";
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
   * What to say when there is nothing, and where to go about it. The two
   * screens have different answers: an empty "mine" sends the reader to the
   * open list, and an empty open list has nowhere useful to send anybody.
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
          {/* `empty`, never `empty-filtered`: neither screen has filters, so
              there is no control to offer and offering one would be a lie. */}
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
                    <Tag tone={STATUS_TONE[contest.status]}>{shared.status[contest.status]}</Tag>
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
                    {canOfferToJoin(contest) ? (
                      <EnrollButton contestId={contest.id} dict={dict} />
                    ) : canOpen(contest) ? (
                      /* The one row on this screen with something to do right
                         now. It outranks "you are enrolled", which is a state
                         rather than a step — and a contest that is running is
                         the only thing a participant came here for. */
                      <Link
                        href={`/contests/${contest.id}/play`}
                        className={cn(buttonVariants({ variant: "primary", size: "sm" }))}
                      >
                        {t.openConsole}
                      </Link>
                    ) : contest.enrolled ? (
                      /* First, because it outranks every other reason there is
                         nothing to press. "By invitation" on a contest one is
                         already invited to reads as though one were not. */
                      <span className="text-small text-ink-2">{t.enrolled}</span>
                    ) : (
                      /* Nothing to do, and the state column already says why.
                         A disabled button repeating it would be a control that
                         exists only to be refused. */
                      <span className="text-small text-ink-3">
                        {contest.enrollment === "invite_only" ? t.byInvitation : t.noAction}
                      </span>
                    )}
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
