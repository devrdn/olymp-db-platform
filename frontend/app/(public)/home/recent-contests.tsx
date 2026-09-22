import Link from "next/link";

import { Band } from "@/components/layout/band";
import { ContestWindow } from "@/components/product/contest-window";
import { DrawnCover } from "@/components/product/drawn-cover";
import { StateView } from "@/components/product/state-view";
import { Tag } from "@/components/ui/tag";
import { coverHref, type ContestStatus } from "@/lib/api/contests";
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
 * Cards with pictures rather than the rows this section started as. The
 * surfaces differ on purpose: the organiser's register is a register — a
 * numbered row with a state and metrics, and no thumbnails (design spec §10) —
 * while this page has one job, which is to interest somebody who has never
 * been here. What a card says is still what a row said: a name, a state, a
 * window, and a way to the table when the table is open. There is no result
 * and no place, because there is nobody here to have one.
 *
 * Still no box. The card is a cover and the text under it, held apart by the
 * same hairline that holds every other division on the page, because a border
 * and a shadow around content is the wall of cards this direction was chosen
 * to get away from.
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
 * How many cards the page will print.
 *
 * The API sends at most this many, and the list still counts them. A front
 * page whose whole argument is that it is short should not be able to grow a
 * screen of cards because a server-side constant moved.
 */
const MOST = 3;

/**
 * The rendition a card asks for, and the size it reserves for it.
 *
 * 800 × 450 is what the server stores for this surface; sending the 1600 one
 * to a card four hundred pixels wide spends a school's Wi-Fi on detail nobody
 * can see. The numbers are written onto the element as well as asked for in
 * the address, because a browser that knows the aspect before the bytes
 * arrive does not move the page under somebody's finger when they do.
 */
const CARD_COVER = { size: 800, height: 450 } as const;

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
        /* Three across where there is room, two on a tablet, one on a phone.
           The section's own rule stays above them, so the grid begins where
           the list used to. */
        <ul className="grid grid-cols-1 gap-x-8 gap-y-10 border-t border-line pt-8 narrow:grid-cols-2 wide:grid-cols-3">
          {rows.map((contest) => (
            <Card key={contest.id} contest={contest} dict={dict} locale={locale} />
          ))}
        </ul>
      )}
    </Band>
  );
}

function Card({
  contest,
  dict,
  locale,
}: {
  contest: PublicContest;
  dict: Dictionary;
  locale: Locale;
}) {
  const shared = dict.contests;
  const t = dict.home.contests;
  /* The name of a contest with no name of its own is the product's phrase for
     one, rather than an empty line of nothing. */
  const title = contest.title || shared.untitled;

  return (
    /* `group` so that the two movements below answer the card rather than the
       one element under the cursor: a title that underlined only when the
       pointer was on the four words of it would feel like a fault. Both run
       for --t-input, which collapses to a millisecond under a stated
       preference for less motion — there is nothing further to declare. */
    <li className="group flex min-w-0 flex-col gap-4">
      <div className="relative aspect-video w-full overflow-hidden rounded-frame transition-transform duration-(--t-input) ease-standard group-hover:-translate-y-1">
        {contest.coverHash ? (
          /* Not next/image: these bytes come from the API behind the same
             proxy the rest of this app talks to, already cropped, resized and
             re-encoded by the server that stored them, and cached for a year
             at an address that carries their hash. There is nothing left for
             an optimiser to do except put a second cache in front of it. */
          /* eslint-disable-next-line @next/next/no-img-element */
          <img
            src={coverHref(contest.id, contest.coverHash, CARD_COVER.size)}
            alt={t.coverOf.replace("{title}", title)}
            loading="lazy"
            decoding="async"
            width={CARD_COVER.size}
            height={CARD_COVER.height}
            className="photograph size-full object-cover"
          />
        ) : (
          /* Not an empty frame and not a grey rectangle: a cover of the same
             family, so that a row of six in which two organisers uploaded a
             photograph still reads as one row (design spec §2.3). */
          <DrawnCover seed={contest.id} />
        )}

        {/* The title does not sit on the picture but on a scrim resolving to
            the page's own ground (design spec §10.2). This is not cosmetics:
            the organiser chooses the subject, and the system owes the title
            its contrast whatever they chose. */}
        <div
          aria-hidden
          className="absolute inset-0 bg-linear-to-t from-scrim-a from-0% via-scrim-b via-38% to-transparent to-76%"
        />

        <h3 className="absolute inset-x-0 bottom-0 px-4 pb-3 text-row text-ink">
          {/* Not a link. A stranger has no way into a contest from here: the
              contest's own screens need a session and the table has its own
              link below, so a name that navigated would navigate to the
              sign-in form. */}
          <span className="underline decoration-transparent underline-offset-4 transition-colors duration-(--t-input) ease-standard group-hover:decoration-ink">
            {title}
          </span>
        </h3>
      </div>

      <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-3 border-t border-line pt-4">
        <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1.5">
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

        {/* The one thing a card can offer, and only where it exists. A frozen
            or unopened table is a result that is not public, not a missing
            one, and a link that had to be followed to discover that is worse
            than no link at all. */}
        {contest.tableOpen ? (
          <Link
            href={`/contests/${contest.id}/leaderboard`}
            /* Six cards carry this link, and heard one after another
               "Results, Results, Results" names nothing. The accessible name
               carries the contest; the visible word stays the short one. */
            aria-label={t.tableOf.replace("{title}", contest.title)}
            className="text-control text-ink-2 underline decoration-line-2 underline-offset-4 transition-colors duration-(--t-input) ease-standard hover:text-ink hover:decoration-ink"
          >
            {t.table}
          </Link>
        ) : null}
      </div>

      {/* Somebody else's work, credited. An uploaded picture is not published
          without the line saying whose (design spec §10.1); a drawn cover has
          none to carry, because its author is us. */}
      {contest.coverHash && contest.coverAttribution ? (
        <p className="text-small text-ink-3">{contest.coverAttribution}</p>
      ) : null}
    </li>
  );
}
