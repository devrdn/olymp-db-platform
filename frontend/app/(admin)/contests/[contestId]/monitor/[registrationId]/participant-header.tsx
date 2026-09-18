import Link from "next/link";

import { ExportMenu } from "@/components/product/export-menu";
import { MONITOR_FLAGS, participantCsvHref, type MonitorFlags, type Participant } from "@/lib/api/monitor";
import { formatMoment } from "@/lib/format/datetime";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { FlagBadges } from "../participants-table";

const TERM = "font-mono text-label text-ink-3 uppercase";

/**
 * Who the page is about: name and login, registration status, when their
 * clock started and when they finished, their flags as the participants
 * table shows them, their CSV, and the way back to the whole contest.
 *
 * `flags` is null when the participants table did not carry this
 * registration (a contest past the table's bound): the heading then says
 * nothing about flags rather than "none".
 */
export function ParticipantHeader({
  contestId,
  participant,
  flags,
  dict,
  locale,
}: {
  contestId: string;
  participant: Participant;
  flags: MonitorFlags | null;
  dict: Dictionary;
  locale: string;
}) {
  const t = dict.workspace.monitor.participant;
  const statuses = dict.workspace.people.registration as Record<string, string>;
  const anyFlag = flags !== null && MONITOR_FLAGS.some((flag) => flags[flag]);

  return (
    <header className="flex flex-wrap items-start justify-between gap-x-6 gap-y-4">
      <div className="flex max-w-body min-w-0 flex-col gap-2">
        <Link
          href={`/contests/${contestId}/monitor`}
          className="self-start text-small text-ink-2 underline-offset-4 hover:text-ink hover:underline"
        >
          <span aria-hidden>← </span>
          {t.back}
        </Link>
        <h2 className="text-h3 break-words text-ink">{participant.fullName || participant.login}</h2>
        <p className="font-mono text-label break-all text-ink-3">{participant.login}</p>
        <dl className="mt-2 flex flex-wrap gap-x-8 gap-y-3">
          <div className="flex flex-col gap-1">
            <dt className={TERM}>{t.status}</dt>
            <dd className="text-small text-ink">{statuses[participant.status] ?? participant.status}</dd>
          </div>
          <div className="flex flex-col gap-1">
            <dt className={TERM}>{t.clock}</dt>
            <dd className="text-small text-ink-2">
              {participant.startedAt
                ? t.started.replace("{time}", formatMoment(participant.startedAt, { locale }))
                : t.notStarted}
            </dd>
            <dd className="text-small text-ink-2">
              {participant.finishedAt
                ? t.finished.replace("{time}", formatMoment(participant.finishedAt, { locale }))
                : t.notFinished}
            </dd>
          </div>
          {flags !== null ? (
            <div className="flex min-w-0 flex-col gap-1">
              <dt className={TERM}>{t.flags}</dt>
              <dd className="text-small text-ink-2">
                {anyFlag ? <FlagBadges flags={flags} t={dict.workspace.monitor} /> : t.noFlags}
              </dd>
            </div>
          ) : null}
        </dl>
      </div>
      <ExportMenu
        heading={dict.workspace.monitor.export.heading}
        formats={[{ format: "CSV", href: participantCsvHref(contestId, participant.registrationId), label: t.csvLabel }]}
      />
    </header>
  );
}
