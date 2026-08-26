import { ContestWindow } from "@/components/product/contest-window";
import { StateView } from "@/components/product/state-view";
import { Tag } from "@/components/ui/tag";
import type { ContestStatus, ContestSummary } from "@/lib/api/contests";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { EnrollButton } from "./enroll-button";

/**
 * The participant's own list: contests they take part in, and the open ones
 * they may still join. The API returns both in one scoped result set.
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
 * The listing cannot say whether this account is already enrolled — the
 * summary carries no such field, and the scoped result mixes "yours" with
 * "open to you". So the button is offered wherever joining is *possible* and
 * the API decides: a second attempt comes back `already_enrolled`, which the
 * button reports as the accepted answer it is.
 *
 * `draft` and `archived` never appear in a participant's scope. `finished`
 * cannot be joined, and `running` is the game loop's to open, which is step 5.
 */
function canOfferToJoin(contest: ContestSummary): boolean {
  return contest.enrollment === "open" && contest.status === "published";
}

export function ParticipantRegister({
  contests,
  total,
  dict,
  locale,
}: {
  contests: ContestSummary[];
  total: number;
  dict: Dictionary;
  locale: Locale;
}) {
  const t = dict.participant;
  const shared = dict.contests;

  return (
    <section aria-labelledby="my-heading" className="flex flex-col">
      <div className="flex flex-wrap items-baseline justify-between gap-x-6 gap-y-2 pb-6">
        <h1 id="my-heading" className="text-h2 text-ink">
          {t.heading}
        </h1>
        <span className="font-mono text-data text-ink-3">
          {total} {t.countLabel}
        </span>
      </div>

      {contests.length === 0 ? (
        <div className="border-t border-line">
          {/* `empty`, never `empty-filtered`: this screen has no filters, so
              there is no control to offer and offering one would be a lie. */}
          <StateView state={{ kind: "empty", title: t.empty.title, body: t.empty.body }} />
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
