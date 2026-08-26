import Link from "next/link";

import { Band } from "@/components/layout/band";
import { ContestWindow } from "@/components/product/contest-window";
import { Tag } from "@/components/ui/tag";
import { titleIn, type ContestStatus } from "@/lib/api/contests";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { ContestTabs, type Tab } from "./contest-tabs";
import { loadContest } from "./contest";

/** One tone per state, and the accent spent only on what is happening now. */
const STATUS_TONE: Record<ContestStatus, "live" | "good" | "mute"> = {
  draft: "mute",
  published: "good",
  running: "live",
  finished: "mute",
  archived: "mute",
};

export async function generateMetadata(props: LayoutProps<"/contests/[contestId]">) {
  const [{ contestId }, locale] = await Promise.all([props.params, activeLocale()]);
  const contest = await loadContest(contestId);

  return { title: titleIn(contest, locale) };
}

/**
 * The frame every screen of one contest wears: which contest, what state it is
 * in, and the way between its sections.
 *
 * The contest is loaded here rather than in each page. A layout and its page
 * render in the same pass and `fetch` is deduplicated across them, so the
 * heading and the section below it are guaranteed to be describing the same
 * revision of the same contest — which two independent requests do not
 * guarantee, and the symptom of that is a title that disagrees with the state
 * badge beside it.
 *
 * The header is a band and the section below it is another. That keeps the
 * hatched margins running unbroken down the page, and it means a section can
 * fill its own band without inheriting padding meant for the title.
 */
export default async function ContestLayout(props: LayoutProps<"/contests/[contestId]">) {
  const [{ contestId }, locale, dict] = await Promise.all([
    props.params,
    activeLocale(),
    activeDictionary(),
  ]);

  const contest = await loadContest(contestId);
  const t = dict.workspace;
  const base = `/contests/${contest.id}`;

  const tabs: Tab[] = [
    { href: base, label: t.tabs.overview, exact: true },
    { href: `${base}/story`, label: t.tabs.story },
    { href: `${base}/questions`, label: t.tabs.questions },
    { href: `${base}/people`, label: t.tabs.people },
    { href: `${base}/settings`, label: t.tabs.settings },
  ];

  return (
    <>
      {/* No bottom padding: the tab row reaches the band's own rule and the
          current section's mark sits on it. */}
      <Band className="gap-6 pt-9 pb-0">
        <Link
          href="/contests"
          className="w-fit font-mono text-data text-ink-3 transition-colors duration-(--t-input) ease-standard hover:text-ink"
        >
          {t.backToRegister}
        </Link>

        <div className="flex flex-wrap items-start justify-between gap-x-8 gap-y-4">
          <div className="flex min-w-0 flex-col gap-3">
            <h1 className="max-w-head text-h2 text-balance text-ink">
              {/* A draft has no title until somebody writes one, and a blank
                  heading tells the author nothing about what they have open. */}
              {titleIn(contest, locale) || (
                <span className="text-ink-3">{t.untitled}</span>
              )}
            </h1>
            <div className="flex flex-wrap items-center gap-3">
              <Tag tone={STATUS_TONE[contest.status]}>{dict.contests.status[contest.status]}</Tag>
              <span className="font-mono text-data text-ink-3">
                {dict.contests.mode[contest.questionMode]}
              </span>
              <span aria-hidden className="h-3 w-px bg-line-2" />
              <span className="font-mono text-data text-ink-2">
                <ContestWindow
                  startsAt={contest.startsAt}
                  endsAt={contest.endsAt}
                  locale={locale}
                  unscheduled={dict.contests.unscheduled}
                  until={dict.contests.until}
                />
              </span>
            </div>
          </div>
        </div>

        <ContestTabs tabs={tabs} />
      </Band>

      {props.children}
    </>
  );
}
