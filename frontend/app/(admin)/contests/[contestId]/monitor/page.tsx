import { feedSchema, MAX_FEED_PAGE, rosterSchema } from "@/lib/api/monitor";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { loadContest, loadContestResource } from "../contest";
import { MonitorView } from "./monitor-view";

/**
 * What the contest's participants are doing (design §6): the table and the
 * live feed, first read here on the server so the screen arrives filled, then
 * kept current from the browser (`use-monitor.ts`).
 *
 * The monitoring routes answer 403 to anybody without contest.monitor on this
 * contest, and `loadContestResource` answers that as a 404 page — the same
 * as for a contest the viewer may not see at all. The navigation hides the
 * tab from them too (`may_monitor`), but this is the check that holds.
 */
export default async function MonitorPage(props: PageProps<"/contests/[contestId]/monitor">) {
  const [{ contestId }, locale, dict] = await Promise.all([props.params, activeLocale(), activeDictionary()]);

  const [contest, roster, feed] = await Promise.all([
    loadContest(contestId),
    loadContestResource(contestId, "/monitor/participants", (payload) => rosterSchema.parse(payload)),
    // The newest page, which the browser then polls after.
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
