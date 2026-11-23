import Link from "next/link";
import { notFound } from "next/navigation";

import { Band } from "@/components/layout/band";
import { ContestWindow } from "@/components/product/contest-window";
import { PublicStandings } from "@/components/product/public-standings";
import { Tag } from "@/components/ui/tag";
import { archiveContestSchema, type ArchiveContest } from "@/lib/api/archive";
import { branding } from "@/lib/api/branding";
import { outcome } from "@/lib/api/outcome";
import { CONTEST_STATUS_TONE } from "@/lib/api/contests-terms";
import { isId } from "@/lib/api/ids";
import { standingsSchema, type Standings } from "@/lib/api/leaderboard";
import { readPage, Refusal } from "@/lib/api/page-read";
import { serverRequest } from "@/lib/api/server";
import type { Locale } from "@/lib/i18n/config";
import { messageForCode } from "@/lib/i18n/errors";
import { plural } from "@/lib/i18n/plural";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { ArchiveTasks } from "./archive-tasks";

type Table = { kind: "ok"; standings: Standings } | { kind: "refused"; code: string };

type Loaded = { kind: "ok"; contest: ArchiveContest; table: Table } | { kind: "refused"; code: string };

async function load(contestId: string, locale: Locale): Promise<Loaded> {
  // Before any request: an archive that is off, or an id that is not one.
  if (!(await branding()).archive || !isId(contestId)) notFound();

  // Independent, so together, and settled apart: the table is the one the
  // leaderboard page reads, on a budget of its own, and its refusal must not
  // cost the reader the contest: every failure of it is shown in its place.
  const [contest, read] = await Promise.all([
    readPage(`/archive/contests/${contestId}?lang=${locale}`, (payload) => archiveContestSchema.parse(payload), {
      here: `/archive/${contestId}`,
      shown: ["archive_too_often"],
    }),
    outcome(async () => standingsSchema.parse(await serverRequest(`/contests/${contestId}/leaderboard?lang=${locale}`))),
  ]);

  if (contest instanceof Refusal) return { kind: "refused", code: contest.code };
  const table: Table = read.ok ? { kind: "ok", standings: read.value } : { kind: "refused", code: read.code };
  return { kind: "ok", contest, table };
}

/**
 * Reads nothing: metadata also runs for a prefetch and a crawler, and neither
 * may spend the reader's archive budget for a title.
 */
export async function generateMetadata() {
  const dict = await activeDictionary();
  // The table names people.
  return { title: dict.archive.heading, robots: { index: false, follow: false } };
}

export default async function ArchiveContestPage(props: PageProps<"/archive/[contestId]">) {
  const [{ contestId }, dict, locale] = await Promise.all([props.params, activeDictionary(), activeLocale()]);
  const loaded = await load(contestId, locale);
  const t = dict.archive;

  const back = (
    <Link href="/archive" className="text-small text-accent underline underline-offset-4">
      {t.back}
    </Link>
  );

  if (loaded.kind === "refused") {
    return (
      <Band fill className="flex flex-col gap-6 py-12">
        {back}
        <p role="status" className="text-body text-warn">
          {messageForCode(loaded.code, dict.errors)}
        </p>
      </Band>
    );
  }

  const { contest, table } = loaded;

  return (
    <Band fill className="flex flex-col gap-10 py-12">
      <header className="flex flex-col items-start gap-3">
        {back}
        <h1 className="max-w-body text-h2 text-ink">{contest.title}</h1>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          <Tag tone={CONTEST_STATUS_TONE[contest.status]}>{dict.contests.status[contest.status]}</Tag>
          <span className="font-mono text-data text-ink-2">
            <ContestWindow
              startsAt={contest.startsAt}
              endsAt={contest.endsAt}
              locale={locale}
              unscheduled={dict.contests.unscheduled}
              until={dict.contests.until}
            />
          </span>
          <span className="font-mono text-data text-ink-3">{plural(t.participants, contest.participants, locale)}</span>
        </div>
        {contest.description ? <p className="max-w-body text-body text-ink-2">{contest.description}</p> : null}
      </header>

      <section id="results" aria-labelledby="archive-results" className="flex flex-col gap-4">
        <h2 id="archive-results" className="text-h3 text-ink">
          {t.results}
        </h2>
        {/* A finished contest is not archived yet, so its table can still be corrected. */}
        {contest.status === "finished" ? <p className="text-small text-ink-3">{t.provisional}</p> : null}
        <PublicStandings
          contestId={contestId}
          initial={table.kind === "ok" ? table.standings : undefined}
          // Only the table's vocabulary crosses to the client.
          dict={{ leaderboard: dict.leaderboard }}
          locale={locale}
          placeholder={
            table.kind === "refused" ? (
              <p role="status" className="text-body text-warn">
                {messageForCode(table.code, dict.errors)}
              </p>
            ) : null
          }
        />
      </section>

      {contest.tasks ? <ArchiveTasks tasks={contest.tasks} dict={dict} locale={locale} /> : null}
    </Band>
  );
}
