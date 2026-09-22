import Link from "next/link";

import { Band } from "@/components/layout/band";
import { ContestWindow } from "@/components/product/contest-window";
import { StateView } from "@/components/product/state-view";
import { Tag } from "@/components/ui/tag";
import type { ContestStatus } from "@/lib/api/contests";
import type { PublicContest } from "@/lib/api/showcase";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";

/**
 * What this installation is running, and what it has just run.
 *
 * The section the hero's second action points at, which is why it carries the
 * `contests` anchor: a visitor without a session cannot be sent to the
 * catalogue at `/open`, because that is behind sign-in and they would meet the
 * login form where they asked for a list. They get this instead.
 *
 * Rows on rules, the same shape as the profile's list, down to the running
 * contest's accent dot: somebody who follows one of these rows into the
 * product should find the product built out of what they were just looking at
 * (design §2.6). What is different is how little a row says — a name, a state,
 * a window, and a way to the table when the table is open. There is no result
 * and no place here, because there is nobody to have one: this list is read by
 * strangers, and everything on it is already public.
 */

/** One tone per state, and the accent spent only on what is happening now. */
const STATUS_TONE: Record<ContestStatus, "live" | "good" | "mute"> = {
  draft: "mute",
  published: "good",
  running: "live",
  finished: "mute",
  archived: "mute",
};

/**
 * How many rows the page will print.
 *
 * The API sends at most this many, and the list still counts them. A front
 * page whose whole argument is that it is short should not be able to grow a
 * screen of rows because a server-side constant moved.
 */
const MOST = 6;

export function RecentContests({
  contests,
  signedIn,
  dict,
  locale,
}: {
  /** `null` is a failed read; it reaches the same state an empty list does. */
  contests: PublicContest[] | null;
  dict: Dictionary;
  locale: Locale;
  /** Whether the visitor has a session, which decides what the empty state can honestly offer. */
  signedIn: boolean;
}) {
  const t = dict.home.contests;
  /**
   * A failed read and an empty installation are one state here, and on
   * purpose. The profile tells them apart because its reader is signed in,
   * knows what they asked for and can try again; a stranger on the front page
   * can do none of the three, so "the read failed" is not information to them
   * — it is an apology for something they did not ask for. The sentence below
   * is written to be true either way, and the way on is true either way too.
   */
  const rows = (contests ?? []).slice(0, MOST);

  return (
    <Band id="contests" className="gap-7">
      <h2 className="text-h3 text-ink">{t.heading}</h2>

      {rows.length === 0 ? (
        /* `empty`, never `empty-filtered`: this list has no filters, so there
           is no control to offer and offering one would be a lie. The
           catalogue is behind sign-in, which is the point — `/open` takes a
           visitor to the form and then on to the list they asked for, rather
           than leaving them on an accurate screen with nothing to do
           (principle 4). */
        <div className="border-t border-line">
          <StateView
            state={{
              kind: "empty",
              title: t.empty.title,
              body: t.empty.body,
              /* The catalogue is behind sign-in, and `/open` carries the
                 visitor through the form and on to it (`?next=`). What the
                 label may not do is promise a catalogue and produce a form:
                 signed out, it says which door this is. */
              action: {
                label: signedIn ? t.empty.action : t.empty.actionSignedOut,
                href: "/open",
              },
            }}
          />
        </div>
      ) : (
        <ul className="flex flex-col border-t border-line">
          {rows.map((contest) => (
            <Row key={contest.id} contest={contest} dict={dict} locale={locale} />
          ))}
        </ul>
      )}
    </Band>
  );
}

function Row({
  contest,
  dict,
  locale,
}: {
  contest: PublicContest;
  dict: Dictionary;
  locale: Locale;
}) {
  const shared = dict.contests;

  return (
    <li className="flex flex-wrap items-baseline justify-between gap-x-8 gap-y-3 border-b border-line py-5 transition-colors duration-(--t-input) ease-standard hover:bg-panel">
      <div className="flex min-w-0 flex-col gap-2">
        {/* Not a link. A stranger has no way into a contest from here: the
            contest's own screens need a session and the table has its own
            link on the right, so a name that navigated would navigate to the
            sign-in form. The name of a contest with no name of its own is the
            product's phrase for one, rather than an empty line of nothing. */}
        <span className="max-w-head text-row text-ink">{contest.title || shared.untitled}</span>

        <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
          <Tag tone={STATUS_TONE[contest.status]}>{shared.status[contest.status]}</Tag>
          <span className="font-mono text-data text-ink-3">
            <ContestWindow
              startsAt={contest.startsAt}
              endsAt={contest.endsAt}
              locale={locale}
              unscheduled={shared.unscheduled}
              until={shared.until}
            />
          </span>
        </div>
      </div>

      {/* The one thing a row can offer, and only where it exists. A frozen or
          unopened table is a result that is not public, not a missing one, and
          a link that had to be followed to discover that is worse than no
          link at all. */}
      {contest.tableOpen ? (
        <Link
          href={`/contests/${contest.id}/leaderboard`}
          /* Six rows carry this link, and heard one after another "Results,
             Results, Results" names nothing. The accessible name carries the
             contest; the visible word stays the short one. */
          aria-label={dict.home.contests.tableOf.replace("{title}", contest.title)}
          className="text-control text-ink-2 underline decoration-line-2 underline-offset-4 transition-colors duration-(--t-input) ease-standard hover:text-ink hover:decoration-ink"
        >
          {dict.home.contests.table}
        </Link>
      ) : null}
    </li>
  );
}
