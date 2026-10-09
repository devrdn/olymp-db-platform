import { feedSchema, MAX_FEED_PAGE, rosterSchema } from "@/lib/api/monitor";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { loadContest, loadContestResource } from "../contest";
import { MonitorView } from "./monitor-view";

/**
 * The monitoring screen (SPEC.md §5.1), first read on the server, then polled by
 * `use-monitor.ts`. Without contest.monitor the routes answer 403, shown as a
 * 404 page; hiding the tab is cosmetic, this is the check that holds.
 */
export default async function MonitorPage(props: PageProps<"/contests/[contestId]/monitor">) {
  const [{ contestId }, locale, dict] = await Promise.all([props.params, activeLocale(), activeDictionary()]);

  const [contest, roster, feed] = await Promise.all([
    loadContest(contestId),
    loadContestResource(contestId, "/monitor/participants", (payload) => rosterSchema.parse(payload)),
    // The newest page; the browser polls after it.
    loadContestResource(contestId, `/monitor/feed?limit=${MAX_FEED_PAGE}`, (payload) => feedSchema.parse(payload)),
  ]);

  return (
    <MonitorView
      contestId={contest.id}
      roster={roster ?? { generatedAt: "", truncated: false, rows: [] }}
      feed={feed ?? { items: [], more: false }}
      dict={dict}
      locale={locale}
    />
  );
}
