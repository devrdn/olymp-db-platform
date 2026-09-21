import Link from "next/link";
import { notFound, redirect } from "next/navigation";

import { Band } from "@/components/layout/band";
import { ContestWindow } from "@/components/product/contest-window";
import { ExportMenu } from "@/components/product/export-menu";
import { ApiError } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { answersSchema, queriesSchema } from "@/lib/api/journal";
import { myCsvHref, profileReportSchema, profileWorkspaceSchema } from "@/lib/api/profile";
import { serverRequest } from "@/lib/api/server";
import { authRecoveryRedirect } from "@/lib/auth/guard";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { MyAnswers } from "./my-answers";
import { MyNotes } from "./my-notes";
import { MyQueries } from "./my-queries";
import { ReportTabs, tabFromParam, type ReportTab } from "./report-tabs";
import { ResultSummary } from "./result-summary";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.profile.report.tabs.summary };
}

/**
 * One finished contest, as the participant who sat it reads it (design §2.2):
 * what it came to, every query they ran, every answer they gave, and the
 * notes they left.
 *
 * **It opens only for a contest that has ended for this reader.** Somebody
 * else's, one still running, one that never existed — the API answers all
 * three with one 404 and one code, and this page answers all three with the
 * not-found page. Telling them apart would say what exists and who is on it,
 * and a profile that did that would be a way around the rules the contest's
 * own screen keeps while it runs.
 *
 * The tab is in the address, and the server reads that tab's data and only
 * that tab's. The registration is never in the address: it comes from the
 * session on the server's side, so no request from here can name anybody
 * else's.
 */
export default async function ReportPage(props: PageProps<"/profile/contests/[contestId]">) {
  const [{ contestId }, search, locale, dict] = await Promise.all([
    props.params,
    props.searchParams,
    activeLocale(),
    activeDictionary(),
  ]);
  // A segment that is not an identifier is answered here rather than spending
  // a request to be told the same thing.
  if (!isId(contestId)) notFound();
  const tab = tabFromParam(search.tab);

  const [report, panel] = await Promise.all([
    read(contestId, "/report", (payload) => profileReportSchema.parse(payload)),
    loadTab(tab, contestId),
  ]);

  const t = dict.profile.report;
  const shared = dict.contests;
  const statuses = dict.participant.play.workspace.log.status;

  return (
    <Band className="gap-8 pt-9 pb-7">
      <div className="flex min-w-0 flex-col gap-6">
        <div className="flex min-w-0 flex-col gap-3">
          <Link
            href="/profile"
            className="self-start text-control text-ink-3 underline-offset-4 transition-colors duration-(--t-input) ease-standard hover:text-ink hover:underline"
          >
            {t.back}
          </Link>
          <div className="flex min-w-0 flex-wrap items-baseline justify-between gap-x-8 gap-y-3">
            <div className="flex min-w-0 flex-col gap-1.5">
              <h1 className="max-w-head text-h2 text-ink">{report.title}</h1>
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
 * One read under `/me/contests/{id}`, with every 404 answered as the
 * not-found page.
 *
 * A dead session or an account still on its one-time password is not a
 * failure of this page and gets the recovery the guard decides. Everything
 * else is thrown: unlike the profile screen, where two independent sections
 * each survive the other's failure, a report with no report is not a screen.
 */
async function read<T>(contestId: string, path: string, parse: (payload: unknown) => T): Promise<T> {
  try {
    return parse(await serverRequest(`/me/contests/${contestId}${path}`));
  } catch (error: unknown) {
    if (error instanceof ApiError && error.status === 404) notFound();
    const target = authRecoveryRedirect(error, `/profile/contests/${contestId}`);
    if (target) redirect(target);
    throw error;
  }
}

/** What the tab in the address shows, read on the server and nothing else. */
async function loadTab(tab: ReportTab, contestId: string) {
  switch (tab) {
    case "queries":
      return { tab, data: await read(contestId, "/queries", (p) => queriesSchema.parse(p)) } as const;
    case "answers":
      return { tab, data: await read(contestId, "/answers", (p) => answersSchema.parse(p)) } as const;
    case "notes":
      return { tab, data: await read(contestId, "/workspace", (p) => profileWorkspaceSchema.parse(p)) } as const;
    default:
      // The result is the report itself, which the page reads anyway.
      return { tab: "summary" } as const;
  }
}
