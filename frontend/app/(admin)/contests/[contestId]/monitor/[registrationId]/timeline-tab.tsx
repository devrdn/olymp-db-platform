"use client";

import { useId, useState, type FormEvent } from "react";

import { buttonVariants } from "@/components/ui/button";
import type { FeedPage } from "@/lib/api/monitor";
import { instantFromWallClock, wallClockFromInstant } from "@/lib/format/datetime";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { LiveFeed } from "../live-feed";
import { Problem } from "../monitor-view";
import { useMonitor } from "../use-monitor";

const CONTROL = "h-(--control-h) w-full min-w-0 border border-edge bg-bg px-2.5 text-control text-ink";

/**
 * The timeline tab (design §6): everything the participant did, merged, with
 * the contest feed's kind filters and a time range, kept current while the
 * tab is visible — the contest screen's own feed and polling
 * (`LiveFeed`, `useMonitor`), pointed at this participant's timeline.
 *
 * The range is typed as wall-clock time in the zone every time on screen is
 * shown in, and sent as instants; `until` is exclusive.
 */
export function TimelineTab({
  contestId,
  registrationId,
  feed,
  dict,
  locale,
}: {
  contestId: string;
  registrationId: string;
  feed: FeedPage;
  dict: Dictionary;
  locale: string;
}) {
  const monitor = useMonitor({ contestId, participant: registrationId, feed });

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <RangeFilter
        applied={monitor.range}
        onApply={(range) => void monitor.setRange(range)}
        t={dict.workspace.monitor.participant.range}
      />
      <Problem problem={monitor.problem} t={dict.workspace.monitor} className="empty:-mt-4" />
      <LiveFeed
        contestId={contestId}
        feed={monitor.feed}
        kinds={monitor.kinds}
        onKinds={monitor.setKinds}
        onLoadOlder={monitor.loadOlder}
        loadingOlder={monitor.loadingOlder}
        onToLatest={monitor.toLatest}
        showParticipant={false}
        dict={dict}
        locale={locale}
      />
    </div>
  );
}

type Range = { from?: string; until?: string };

function RangeFilter({
  applied,
  onApply,
  t,
}: {
  applied: Range;
  onApply: (range: Range) => void;
  t: Dictionary["workspace"]["monitor"]["participant"]["range"];
}) {
  const ids = useId();
  const [from, setFrom] = useState(() => (applied.from ? wallClockFromInstant(applied.from) : ""));
  const [until, setUntil] = useState(() => (applied.until ? wallClockFromInstant(applied.until) : ""));
  const [invalid, setInvalid] = useState(false);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const range: Range = {
      from: from ? (instantFromWallClock(from) ?? undefined) : undefined,
      until: until ? (instantFromWallClock(until) ?? undefined) : undefined,
    };
    if (range.from && range.until && range.until <= range.from) {
      setInvalid(true);
      return;
    }
    setInvalid(false);
    onApply(range);
  };

  const clear = () => {
    setFrom("");
    setUntil("");
    setInvalid(false);
    if (applied.from || applied.until) onApply({});
  };

  return (
    <form
      onSubmit={submit}
      aria-label={t.label}
      className="flex flex-wrap items-end gap-x-3 gap-y-2"
      noValidate
    >
      <div className="flex min-w-0 grow basis-44 flex-col gap-1.5 sm:max-w-56">
        <label htmlFor={`${ids}-from`} className="font-mono text-label text-ink-3 uppercase">
          {t.from}
        </label>
        <input
          id={`${ids}-from`}
          type="datetime-local"
          value={from}
          onChange={(event) => setFrom(event.target.value)}
          aria-invalid={invalid || undefined}
          className={CONTROL}
        />
      </div>
      <div className="flex min-w-0 grow basis-44 flex-col gap-1.5 sm:max-w-56">
        <label htmlFor={`${ids}-until`} className="font-mono text-label text-ink-3 uppercase">
          {t.until}
        </label>
        <input
          id={`${ids}-until`}
          type="datetime-local"
          value={until}
          onChange={(event) => setUntil(event.target.value)}
          aria-invalid={invalid || undefined}
          aria-describedby={`${ids}-note`}
          className={CONTROL}
        />
      </div>
      <div className="flex gap-2">
        <button type="submit" className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}>
          {t.apply}
        </button>
        <button type="button" onClick={clear} className={cn(buttonVariants({ variant: "quiet", size: "sm" }))}>
          {t.clear}
        </button>
      </div>
      <p id={`${ids}-note`} role="alert" className="w-full text-small text-bad empty:hidden">
        {invalid ? t.invalid : ""}
      </p>
    </form>
  );
}
