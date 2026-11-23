import { RefusalNote } from "@/components/product/refusal-note";
import { isLiveActivity } from "@/lib/api/contests-terms";
import { feedSchema, rosterSchema } from "@/lib/api/monitor";
import { MAX_FEED_PAGE, MONITOR_SHOWN } from "@/lib/api/monitor-terms";
import { Refusal } from "@/lib/api/page-read";
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
    loadContestResource(contestId, "/monitor/participants", (payload) => rosterSchema.parse(payload), {
      shown: MONITOR_SHOWN,
    }),
    // The newest page; the browser polls after it.
    loadContestResource(contestId, `/monitor/feed?limit=${MAX_FEED_PAGE}`, (payload) => feedSchema.parse(payload), {
      shown: MONITOR_SHOWN,
    }),
  ]);

  if (roster instanceof Refusal) return <RefusalNote code={roster.code} errors={dict.errors} />;
  if (feed instanceof Refusal) return <RefusalNote code={feed.code} errors={dict.errors} />;

  return (
    <MonitorView
      contestId={contest.id}
      changing={isLiveActivity(contest.status)}
      roster={roster}
      feed={feed}
      // Every prop of a client component is serialised into the page; the
      // screen reads one section of the dictionary.
      dict={{ workspace: dict.workspace }}
      locale={locale}
    />
  );
}
