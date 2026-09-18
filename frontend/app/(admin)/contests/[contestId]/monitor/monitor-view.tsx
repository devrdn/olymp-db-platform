"use client";

import { ExportMenu } from "@/components/product/export-menu";
import { monitorCsvHref, type FeedPage, type Roster } from "@/lib/api/monitor";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { LiveFeed } from "./live-feed";
import { ParticipantsTable } from "./participants-table";
import { useMonitor, type MonitorProblem } from "./use-monitor";

/**
 * The contest's monitoring screen: the participants table on the left, the
 * live feed on the right (design §6, first item).
 *
 * Side by side only where the workspace column is wide enough for both — a
 * container query, because the column's width depends on the navigation
 * beside it as much as on the window. Narrower, the feed goes under the
 * table. Either way each half scrolls inside its own box, so neither a wide
 * table nor a long line ever widens the page.
 */
export function MonitorView({
  contestId,
  roster,
  feed: initialFeed,
  dict,
  locale,
}: {
  contestId: string;
  roster: Roster;
  feed: FeedPage;
  dict: Dictionary;
  locale: string;
}) {
  const t = dict.workspace.monitor;
  const monitor = useMonitor({ contestId, roster, feed: initialFeed });

  return (
    // The gap here is cancelled by the empty status line's `empty:-mt-8`
    // (Problem, below): change one and the other changes with it.
    <div className="@container flex min-w-0 flex-col gap-8">
      <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-4">
        <div className="flex max-w-body min-w-0 flex-col gap-3">
          <h2 className="text-h3 text-ink">{dict.workspace.tabs.monitor}</h2>
          <p className="text-body text-ink-2">{t.lede}</p>
          <ul className="flex flex-col gap-1 text-small text-ink-3">
            <li>{t.notes.hints}</li>
            <li>{t.notes.delay}</li>
            <li>{t.notes.absenceAtEnd}</li>
          </ul>
        </div>
        <ExportMenu
          heading={t.export.heading}
          formats={[{ format: "CSV", href: monitorCsvHref(contestId), label: t.export.label }]}
        />
      </div>

      <Problem problem={monitor.problem} t={t} />

      <div className="grid min-w-0 gap-x-8 gap-y-10 @min-[54rem]:grid-cols-[minmax(0,1fr)_21rem]">
        <section aria-labelledby="monitor-participants" className="flex min-w-0 flex-col gap-4">
          <h3 id="monitor-participants" className="font-mono text-label text-ink-3 uppercase">
            {t.table.heading}
          </h3>
          <ParticipantsTable
            contestId={contestId}
            rows={monitor.rows}
            truncated={monitor.truncated}
            fresh={monitor.fresh}
            dict={dict}
            locale={locale}
          />
        </section>

        <section aria-labelledby="monitor-feed" className="flex min-w-0 flex-col gap-4">
          <h3 id="monitor-feed" className="font-mono text-label text-ink-3 uppercase">
            {t.feed.heading}
          </h3>
          <LiveFeed
            contestId={contestId}
            feed={monitor.feed}
            kinds={monitor.kinds}
            onKinds={monitor.setKinds}
            onLoadOlder={monitor.loadOlder}
            loadingOlder={monitor.loadingOlder}
            onToLatest={monitor.toLatest}
            dict={dict}
            locale={locale}
          />
        </section>
      </div>
    </div>
  );
}

/**
 * The live region for what went wrong. Always in the document, with only its
 * text changing: a region inserted together with its message is not announced
 * by every screen reader. Shared with one participant's page, whose column
 * keeps the same `gap-8` its empty state takes back.
 */
export function Problem({ problem, t }: { problem: MonitorProblem; t: Dictionary["workspace"]["monitor"] }) {
  const text = !problem
    ? ""
    : problem.kind === "forbidden"
      ? t.problems.forbidden
      : problem.kind === "tooOften"
        ? t.problems.tooOften.replace("{seconds}", String(problem.seconds))
        : t.problems.failed;
  return (
    // Empty, it takes back the column's gap above it (`gap-8` on the view's
    // root — keep the two equal) rather than being
    // hidden: a hidden region is out of the accessibility tree, which is
    // what keeping it in the document is for.
    <p role="status" className="text-small text-warn empty:-mt-8">
      {text}
    </p>
  );
}
