"use client";

import { ExportMenu } from "@/components/product/export-menu";
import { monitorCsvHref, type FeedPage, type Roster } from "@/lib/api/monitor";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { LiveFeed } from "./live-feed";
import { ParticipantsTable } from "./participants-table";
import { useMonitor, type MonitorProblem } from "./use-monitor";

/**
 * The monitoring screen: participants table and live feed (SPEC.md §5.1). Side by
 * side when a container query says the column is wide enough (it depends on the
 * navigation too), stacked otherwise. Each half scrolls inside its own box.
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
    // This gap and `Problem`'s `empty:-mt-8` must stay equal.
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
 * The live region for errors. Always rendered with only its text changing,
 * since a region inserted with its message is not announced by every screen
 * reader.
 */
export function Problem({
  problem,
  t,
  className = "empty:-mt-8",
}: {
  problem: MonitorProblem;
  t: Dictionary["workspace"]["monitor"];
  /** How the empty line takes back the gap above; the default takes back `gap-8`. */
  className?: string;
}) {
  const text = !problem
    ? ""
    : problem.kind === "forbidden"
      ? t.problems.forbidden
      : problem.kind === "tooOften"
        ? t.problems.tooOften.replace("{seconds}", String(problem.seconds))
        : t.problems.failed;
  return (
    // Collapsed when empty rather than hidden, so it stays in the accessibility
    // tree.
    <p role="status" className={cn("text-small text-warn", className)}>
      {text}
    </p>
  );
}
