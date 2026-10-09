import Link from "next/link";

import { Band } from "@/components/layout/band";
import { ContestWindow } from "@/components/product/contest-window";
import { DrawnCover } from "@/components/product/drawn-cover";
import { StateView } from "@/components/product/state-view";
import { Tag } from "@/components/ui/tag";
import { coverHref } from "@/lib/api/contests";
import type { PublicContest } from "@/lib/api/showcase";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { CONTEST_STATUS_TONE } from "@/lib/api/contests-terms";

/**
 * Recent and running contests on the front page, at the `contests` anchor the
 * hero links to (the `/open` catalogue is behind sign-in). Cards with covers,
 * unlike the organiser's register, because this page exists to interest a
 * newcomer; a card says what a row would: name, state, window, and the table
 * when open.
 */

/**
 * Most cards shown. The API sends at most this many, but the page must stay
 * short even if that constant moves.
 */
const MOST = 3;

/**
 * The 800 × 450 rendition the server stores for cards, also written onto the
 * element so the browser reserves the space before the bytes arrive.
 */
const CARD_COVER = { size: 800, height: 450 } as const;

export function RecentContests({
  contests,
  signedIn,
  dict,
  locale,
}: {
  /** `null` is a failed read, shown like an empty list. */
  contests: PublicContest[] | null;
  dict: Dictionary;
  locale: Locale;
  /** Whether the visitor has a session, which decides what the empty state offers. */
  signedIn: boolean;
}) {
  const t = dict.home.contests;
  /**
   * A failed read and an empty installation are one state here: a stranger
   * cannot act on the difference, and the sentence is true of both.
   */
  const rows = (contests ?? []).slice(0, MOST);

  return (
    <Band id="contests" className="gap-7">
      <h2 className="text-h3 text-ink">{t.heading}</h2>

      {rows.length === 0 ? (
        /* No filters to reset; the next step is the catalogue via `/open`
           (SPEC.md §2, principle 4). */
        <div className="border-t border-line">
          <StateView
            state={{
              kind: "empty",
              title: t.empty.title,
              body: t.empty.body,
              /* Signed out, the label names the sign-in door rather than promising a catalogue. */
              action: {
                label: signedIn ? t.empty.action : t.empty.actionSignedOut,
                href: "/open",
              },
            }}
          />
        </div>
      ) : (
        /* Three columns, two on a tablet, one on a phone. */
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
  /* The product's phrase for an untitled contest. */
  const title = contest.title || shared.untitled;

  return (
    /*
     * `group`, so hover effects respond to the whole card. `--t-input` already
     * collapses under reduced motion.
     */
    <li className="group flex min-w-0 flex-col gap-4">
      <div className="relative aspect-video w-full overflow-hidden rounded-frame transition-transform duration-(--t-input) ease-standard group-hover:-translate-y-1">
        {contest.coverHash ? (
          /*
           * Not next/image: the API already resized and re-encoded the bytes
           * and caches them for a year at a hashed address.
           */
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
          /* A drawn cover, so a mixed row reads as one row (SPEC.md §10.3). */
          <DrawnCover seed={contest.id} />
        )}

        {/* The title sits on a scrim (SPEC.md §10.2), guaranteeing contrast
           whatever picture the organiser chose. */}
        <div
          aria-hidden
          className="absolute inset-0 bg-linear-to-t from-scrim-a from-0% via-scrim-b via-38% to-transparent to-76%"
        />

        <h3 className="absolute inset-x-0 bottom-0 px-4 pb-3 text-row text-ink">
          {/* Not a link: a contest's screens need a session, so it would lead to sign-in. */}
          <span className="underline decoration-transparent underline-offset-4 transition-colors duration-(--t-input) ease-standard group-hover:decoration-ink">
            {title}
          </span>
        </h3>
      </div>

      <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-3 border-t border-line pt-4">
        <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1.5">
          <Tag tone={CONTEST_STATUS_TONE[contest.status]}>{shared.status[contest.status]}</Tag>
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

        {/* Only when the table is open; a frozen or unopened table is not public. */}
        {contest.tableOpen ? (
          <Link
            href={`/contests/${contest.id}/leaderboard`}
            /*
             * The accessible name carries the contest, so repeated "Results"
             * links are distinguishable.
             */
            aria-label={t.tableOf.replace("{title}", contest.title)}
            className="text-control text-ink-2 underline decoration-line-2 underline-offset-4 transition-colors duration-(--t-input) ease-standard hover:text-ink hover:decoration-ink"
          >
            {t.table}
          </Link>
        ) : null}
      </div>

      {/* An uploaded picture is always credited (SPEC.md §10.1); a drawn cover needs none. */}
      {contest.coverHash && contest.coverAttribution ? (
        <p className="text-small text-ink-3">{contest.coverAttribution}</p>
      ) : null}
    </li>
  );
}
