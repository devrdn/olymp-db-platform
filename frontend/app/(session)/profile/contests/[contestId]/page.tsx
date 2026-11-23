import Link from "next/link";
import { notFound } from "next/navigation";

import { Band } from "@/components/layout/band";
import { ContestWindow } from "@/components/product/contest-window";
import { ExportMenu } from "@/components/product/export-menu";
import { RefusalNote } from "@/components/product/refusal-note";
import { isId } from "@/lib/api/ids";
import { answersSchema, queriesSchema } from "@/lib/api/journal";
import { readPage, Refusal } from "@/lib/api/page-read";
import { myCsvHref, profileReportSchema, profileWorkspaceSchema } from "@/lib/api/profile";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { MyAnswers } from "./my-answers";
import { MyNotes } from "./my-notes";
import { MyQueries } from "./my-queries";
import { ReportTabs, tabFromParam, type ReportTab } from "./report-tabs";
import { ResultSummary } from "./result-summary";

/**
 * The tab's name in the title, so several browser tabs are distinguishable. Not
 * the contest name, which would need a read and could reveal a contest the
 * caller may not see.
 */
export async function generateMetadata(props: PageProps<"/profile/contests/[contestId]">) {
  const [dict, search] = await Promise.all([activeDictionary(), props.searchParams]);
  return { title: dict.profile.report.tabs[tabFromParam(search.tab)] };
}

/**
 * A finished contest as its participant reads it (SPEC.md §5.2): result,
 * queries, answers and notes.
 *
 * Only for a contest that has ended for this reader. Someone else's, a running
 * one and a missing one all get the same 404, so the page cannot reveal what
 * exists. The tab is in the address and only its data is read; the registration
 * comes from the session, never the address.
 */
export default async function ReportPage(props: PageProps<"/profile/contests/[contestId]">) {
  const [{ contestId }, search, locale, dict] = await Promise.all([
    props.params,
    props.searchParams,
    activeLocale(),
    activeDictionary(),
  ]);
  // A non-id segment is not worth a request.
  if (!isId(contestId)) notFound();
  const tab = tabFromParam(search.tab);

  // allSettled, not Promise.all: `notFound()` and `redirect()` throw, and with
  // Promise.all a tab's own failure could win over the report's 404 and leave
  // the other rejection unhandled. The report's outcome decides.
  const [reported, panelled] = await Promise.allSettled([
    read(contestId, "/report", (payload) => profileReportSchema.parse(payload)),
    loadTab(tab, contestId),
  ]);
  if (reported.status === "rejected") throw reported.reason;
  if (panelled.status === "rejected") throw panelled.reason;
  const report = reported.value;
  const panel = panelled.value;

  const t = dict.profile.report;
  const shared = dict.contests;
  const statuses = dict.participant.play.workspace.log.status;

  const back = (
    <Link
      href="/profile"
      className="self-start text-control text-ink-3 underline-offset-4 transition-colors duration-(--t-input) ease-standard hover:text-ink hover:underline"
    >
      {t.back}
    </Link>
  );

  const refused = (code: string) => (
    <Band className="gap-6 pt-9 pb-7">
      {back}
      <RefusalNote code={code} errors={dict.errors} />
    </Band>
  );
  if (report instanceof Refusal) return refused(report.code);
  if (panel instanceof Refusal) return refused(panel.code);

  return (
    <Band className="gap-8 pt-9 pb-7">
      <div className="flex min-w-0 flex-col gap-6">
        <div className="flex min-w-0 flex-col gap-3">
          {back}
          <div className="flex min-w-0 flex-wrap items-baseline justify-between gap-x-8 gap-y-3">
            <div className="flex min-w-0 flex-col gap-1.5">
              {/* Wrapped, not truncated, and never wider than the column. */}
              <h1 className="max-w-head text-h2 break-words text-ink">{report.title}</h1>
              <span className="font-mono text-data text-ink-3">
                <ContestWindow
                  startsAt={report.startsAt}
                  endsAt={report.endsAt}
                  locale={locale}
                  unscheduled={shared.unscheduled}
                  until={shared.until}
                />
              </span>
            </div>
            <ExportMenu
              heading={t.csvHeading}
              formats={[{ format: t.csv, href: myCsvHref(contestId), label: t.csvLabel }]}
            />
          </div>
        </div>

        <div className="flex min-w-0 flex-col gap-6">
          <ReportTabs contestId={contestId} current={tab} t={t} />
          <section aria-label={t.tabs[tab]} className="min-w-0">
            {panel.tab === "queries" ? (
              <MyQueries contestId={contestId} initial={panel.data} t={t} statuses={statuses} locale={locale} />
            ) : panel.tab === "answers" ? (
              <MyAnswers answers={panel.data} t={t} statuses={statuses} locale={locale} />
            ) : panel.tab === "notes" ? (
              <MyNotes workspace={panel.data} t={t} />
            ) : (
              <ResultSummary report={report} t={t} locale={locale} />
            )}
          </section>
        </div>
      </div>
    </Band>
  );
}

/**
 * One read under `/me/contests/{id}`. A contest that is not this reader's
 * finished one is `profile_contest_not_found`, the not-found page; the
 * reader's own read budget is shown in words. The report cannot stand without
 * any of them.
 */
function read<T>(contestId: string, path: string, parse: (payload: unknown) => T): Promise<T | Refusal> {
  return readPage(`/me/contests/${contestId}${path}`, parse, {
    here: `/profile/contests/${contestId}`,
    missing: ["profile_contest_not_found"],
    shown: ["profile_too_often"],
  });
}

/** A tab's panel, or the refusal its read met. */
function panelOf<const Tab extends ReportTab, T>(tab: Tab, data: T | Refusal) {
  return data instanceof Refusal ? data : ({ tab, data } as const);
}

/** The current tab's data, read on the server. */
async function loadTab(tab: ReportTab, contestId: string) {
  switch (tab) {
    case "queries":
      return panelOf(tab, await read(contestId, "/queries", (p) => queriesSchema.parse(p)));
    case "answers":
      return panelOf(tab, await read(contestId, "/answers", (p) => answersSchema.parse(p)));
    case "notes":
      return panelOf(tab, await read(contestId, "/workspace", (p) => profileWorkspaceSchema.parse(p)));
    default:
      // The summary is the report, already read.
      return { tab: "summary" } as const;
  }
}
