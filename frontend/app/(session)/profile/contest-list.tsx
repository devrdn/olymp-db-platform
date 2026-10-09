import Link from "next/link";

import { ContestWindow } from "@/components/product/contest-window";
import { StateView } from "@/components/product/state-view";
import { buttonVariants } from "@/components/ui/button";
import { Tag } from "@/components/ui/tag";
import type { ProfileContest, ProfileContests, ProfileResult } from "@/lib/api/profile";
import { formatMoment } from "@/lib/format/datetime";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";
import { CONTEST_STATUS_TONE } from "@/lib/api/contests-terms";

/**
 * Every contest this account is on, newest first, as rows (SPEC.md §5.2).
 * Each row offers one thing.
 *
 * A running contest gets only a way in: its data belongs to the contest screen
 * and its rules (window, network, timer), and repeating it here would bypass
 * them. No place is shown, since that needs a whole table per contest; only
 * whether the table is open.
 */

export function ContestList({
  contests,
  dict,
  locale,
}: {
  /** `null` is a failed read, which costs this section, not the page. */
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
          {/* No filters to reset. */}
          <StateView
            state={{
              kind: "empty",
              title: t.empty.title,
              body: t.empty.body,
              // The state names its next step (SPEC.md §2, principle 4).
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
  // `over` decides, not the status: a participant whose timer ran out or who
  // was disqualified is done while the contest still runs.
  const done = contest.over;

  return (
    <li className="flex flex-wrap items-baseline justify-between gap-x-8 gap-y-3 border-b border-line py-5 transition-colors duration-(--t-input) ease-standard hover:bg-panel">
      <div className="flex min-w-0 flex-col gap-2">
        {done ? (
          /* The name links to the report; the accessible name keeps the title inside it. */
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
          {/* No tag when the contest runs but is over for this reader: the
             accent belongs to a door they can still use. */}
          {done && contest.status === "running" ? null : (
            <Tag tone={CONTEST_STATUS_TONE[contest.status]}>{shared.status[contest.status]}</Tag>
          )}
          {contest.registrationStatus === "disqualified" ? (
            <Tag tone="bad">{t.disqualified}</Tag>
          ) : null}
          {/* Dates only for a finished contest; an upcoming one shows its start on the right. */}
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

      {/* Right-aligned so the figures form a column down the list. */}
      <div className="flex flex-col items-end gap-2 text-right">
        {contest.result !== undefined ? (
          <>
            <Result result={contest.result} dict={dict} />
            {contest.result.placeOpen ? null : (
              <p className="max-w-body font-mono text-data text-ink-3">
                {/* A frozen table will be revealed; a contest that never started has none. */}
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
 * The participant's result. ICPC has no points (the server writes none), so it
 * shows solved and penalty; the penalty's presence identifies the mode.
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

/**
 * A number over its caption. A minimum width fits the longest caption and a
 * five-figure penalty, so rows line up.
 */
function Figure({ value, label }: { value: number; label: string }) {
  return (
    <span className="flex min-w-18 flex-col-reverse gap-1 text-right">
      <span className="font-mono text-label text-ink-3 uppercase">{label}</span>
      <span className="font-mono text-h3 text-ink tabular-nums">{value}</span>
    </span>
  );
}
