import Link from "next/link";

import { ContestWindow } from "@/components/product/contest-window";
import { StateView } from "@/components/product/state-view";
import { buttonVariants } from "@/components/ui/button";
import { Tag } from "@/components/ui/tag";
import type { ContestStatus } from "@/lib/api/contests";
import type { ProfileContest, ProfileContests, ProfileResult } from "@/lib/api/profile";
import { formatMoment } from "@/lib/format/datetime";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

/**
 * Every contest this account is on, newest first, with its own result.
 *
 * Rows on rules, not cards: what a reader does here is run down a column of
 * their own results, and a wall of tiles is what the direction was chosen to
 * get away from (SPEC §5). One row is the contest's name, what state it is in,
 * and the one thing that row can offer — which is a different thing in each of
 * three cases, and never two things at once.
 *
 * **A running contest gets a way in and nothing else.** Not a result, not a
 * report, not a query count: what a participant needs while a contest is on is
 * on the contest's own screen, under that screen's rules about the window, the
 * network and their individual timer (design §1). A profile that repeated any
 * of it would be a second door into the same data past those rules.
 *
 * **There is no place in a row.** The list would have to compute a whole
 * standings table per contest to name one; the report does that for the one
 * contest somebody opens. What a row can say for nothing is whether the table
 * is open at all, and it says so — a frozen table is a result that is not
 * public yet, not a missing one.
 */

/** One tone per state, and the accent spent only on what is happening now. */
const STATUS_TONE: Record<ContestStatus, "live" | "good" | "mute"> = {
  draft: "mute",
  published: "good",
  running: "live",
  finished: "mute",
  archived: "mute",
};

export function ContestList({
  contests,
  dict,
  locale,
}: {
  /** `null` is a failed read, which costs this section and not the page. */
  contests: ProfileContests | null;
  dict: Dictionary;
  locale: Locale;
}) {
  const t = dict.profile.contests;

  return (
    <section aria-labelledby="profile-contests" className="flex flex-col gap-5 border-t border-line pt-6">
      <h2 id="profile-contests" className="font-mono text-label text-ink-3 uppercase">
        {t.heading}
      </h2>

      {contests === null ? (
        <p role="alert" className="max-w-body text-body text-bad">
          {t.failed}
        </p>
      ) : contests.items.length === 0 ? (
        <div className="border-t border-line">
          {/* `empty`, never `empty-filtered`: this list has no filters, so
              there is no control to offer and offering one would be a lie. */}
          <StateView
            state={{
              kind: "empty",
              title: t.empty.title,
              body: t.empty.body,
              // Principle 4: the state names its next step. Without it a new
              // student meets an accurate screen with nothing to do on it.
              action: { label: t.empty.action, href: "/open" },
            }}
          />
        </div>
      ) : (
        <>
          <ul className="flex flex-col border-t border-line">
            {contests.items.map((contest) => (
              <Row key={contest.contestId} contest={contest} dict={dict} locale={locale} />
            ))}
          </ul>
          {contests.truncated ? (
            <p className="font-mono text-data text-ink-3">
              {t.truncated.replace("{count}", String(contests.items.length))}
            </p>
          ) : null}
        </>
      )}
    </section>
  );
}

function Row({
  contest,
  dict,
  locale,
}: {
  contest: ProfileContest;
  dict: Dictionary;
  locale: Locale;
}) {
  const t = dict.profile.contests;
  const shared = dict.contests;
  // `over` decides, never the status. A participant whose own timer ran out,
  // or who was disqualified, is finished with a contest the clock says is
  // still running — and their report is open while everybody else is still
  // working. The result is what such a contest carries; a row that had none
  // is still a finished one, and falls back to saying nothing rather than to
  // the sentence a contest still to come gets.
  const done = contest.over;

  return (
    <li className="flex flex-wrap items-baseline justify-between gap-x-8 gap-y-3 border-b border-line py-5 transition-colors duration-(--t-input) ease-standard hover:bg-panel">
      <div className="flex min-w-0 flex-col gap-2">
        {done ? (
          /* The row leads to the report, and the name is what leads there.
             The accessible name says what the link does and keeps the title
             inside it, so what is heard and what is read still match. */
          <Link
            href={`/profile/contests/${contest.contestId}`}
            aria-label={t.report.replace("{title}", contest.title)}
            className="max-w-head text-row text-ink underline decoration-line-2 underline-offset-4 transition-colors duration-(--t-input) ease-standard hover:decoration-ink"
          >
            {contest.title}
          </Link>
        ) : (
          <span className="max-w-head text-row text-ink">{contest.title}</span>
        )}

        <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
          <Tag tone={STATUS_TONE[contest.status]}>{shared.status[contest.status]}</Tag>
          {contest.registrationStatus === "disqualified" ? (
            <Tag tone="bad">{t.disqualified}</Tag>
          ) : null}
          {/* When it ran, and only on a contest that did. A running one says
              nothing about itself here, and one still to come says when it
              starts on the other side of the row. */}
          {done ? (
            <span className="font-mono text-data text-ink-3">
              <ContestWindow
                startsAt={contest.startsAt}
                endsAt={contest.endsAt}
                locale={locale}
                unscheduled={shared.unscheduled}
                until={shared.until}
              />
            </span>
          ) : null}
        </div>
      </div>

      <div className="flex flex-col items-start gap-2">
        {contest.result !== undefined ? (
          <>
            <Result result={contest.result} dict={dict} />
            {contest.result.placeOpen ? null : (
              <p className="max-w-body font-mono text-data text-ink-3">
                {/* Two reasons for a shut table, and they are not the same
                    sentence. A freeze is a result about to be revealed; a
                    contest that never opened has no table to reveal, and
                    promising one would be a promise nobody is going to
                    keep. */}
                {contest.result.state === "not_started" ? t.placeNotStarted : t.placePending}
              </p>
            )}
          </>
        ) : done ? null : contest.status === "running" ? (
          <Link
            href={`/contests/${contest.contestId}/play`}
            className={cn(buttonVariants({ variant: "primary", size: "sm" }))}
          >
            {t.enter}
          </Link>
        ) : (
          <p className="font-mono text-data text-ink-2">
            {t.starts.replace(
              "{when}",
              contest.startsAt ? formatMoment(contest.startsAt, { locale }) : shared.unscheduled,
            )}
          </p>
        )}
      </div>
    </li>
  );
}

/**
 * The participant's own numbers, in whichever of the two shapes the mode makes
 * a result.
 *
 * ICPC is not "points with extra columns": the server writes no points at all
 * in that mode, so a row that printed them would report nought over four
 * solved questions. The penalty is the field that says which shape this is,
 * because it is sent in that mode and in no other.
 */
function Result({ result, dict }: { result: ProfileResult; dict: Dictionary }) {
  const t = dict.profile.contests;

  return (
    <div className="flex items-baseline gap-6">
      {result.scoring === "icpc" ? (
        <>
          <Figure value={result.solved} label={t.solved} />
          <Figure value={result.penalty ?? 0} label={t.penalty} />
        </>
      ) : (
        <>
          <Figure value={result.points} label={t.points} />
          <Figure value={result.solved} label={t.solved} />
        </>
      )}
    </div>
  );
}

/** One number over its caption, the same pair the summary above is made of. */
function Figure({ value, label }: { value: number; label: string }) {
  return (
    <span className="flex min-w-0 flex-col-reverse gap-1">
      <span className="font-mono text-label text-ink-3 uppercase">{label}</span>
      <span className="font-mono text-h3 text-ink tabular-nums">{value}</span>
    </span>
  );
}
