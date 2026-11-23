"use client";

import { ActionButton, ActionForm, ActionOutcome } from "@/components/product/action-form";
import { Tag } from "@/components/ui/tag";
import { ScrollBox } from "@/components/ui/scroll-box";
import type { GameInstance, GameInstances } from "@/lib/api/game";
import { readableBytes } from "@/lib/format/bytes";
import { formatMoment } from "@/lib/format/datetime";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { dropGameInstanceAction } from "./actions";
import { fill, isolate } from "@/lib/i18n/fill";

const HEAD =
  "border-b border-line-2 px-(--row-px) py-2.5 font-mono text-label font-medium text-ink-3 uppercase";
const CELL = "border-b border-line px-(--row-px) py-(--row-py) align-middle";

const STATUS_TONE: Record<GameInstance["status"], "good" | "warn" | "bad" | "mute"> = {
  provisioning: "warn",
  ready: "good",
  failed: "bad",
  dropped: "mute",
};

/**
 * The contest's databases in one table, spare and held alike, with the holder's
 * name. Only a held copy offers the drop button: the pool tender replaces a
 * broken spare on its own. The route's page supplies the heading.
 *
 * `ended` is true once the contest is finished or archived: the sweep then
 * removes every copy, so an empty list means removed, not not yet built.
 */
export function GameDatabases({
  contestId,
  databases,
  ended,
  locale,
  dict,
}: {
  contestId: string;
  databases: GameInstances;
  ended: boolean;
  locale: Locale;
  dict: Dictionary;
}) {
  const t = dict.workspace.databases;

  return (
    <section className="flex flex-col gap-5">
      {databases.instances.length === 0 ? (
        <p className="max-w-body text-body text-ink-2">{ended ? t.reclaimed : t.empty}</p>
      ) : (
        <ScrollBox>
          <table className="w-full min-w-lg border-collapse text-left">
            <thead>
              <tr>
                <th scope="col" className={HEAD}>
                  {t.columns.database}
                </th>
                <th scope="col" className={HEAD}>
                  {t.columns.holder}
                </th>
                <th scope="col" className={cn(HEAD, "w-24")}>
                  {t.columns.version}
                </th>
                <th scope="col" className={cn(HEAD, "w-28")}>
                  {t.columns.status}
                </th>
                <th scope="col" className={cn(HEAD, "w-24")}>
                  {t.columns.size}
                </th>
                <th scope="col" className={cn(HEAD, "w-40")}>
                  {t.columns.created}
                </th>
                <th scope="col" className={cn(HEAD, "w-24")}>
                  <span className="sr-only">{t.drop}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {databases.instances.map((instance) => (
                <tr key={instance.database}>
                  <td className={cn(CELL, "font-mono text-data text-ink")}>{instance.database}</td>
                  <td className={CELL}>
                    <Holder instance={instance} t={t} />
                  </td>
                  <td className={cn(CELL, "font-mono text-data text-ink-2")}>
                    {instance.templateVersion}
                  </td>
                  <td className={CELL}>
                    <Tag tone={STATUS_TONE[instance.status]}>{t.status[instance.status]}</Tag>
                  </td>
                  <td className={cn(CELL, "font-mono text-data text-ink-2")}>
                    {instance.sizeKnown ? readableBytes(instance.sizeBytes) : t.sizeUnknown}
                  </td>
                  <td className={cn(CELL, "text-small text-ink-2")}>
                    {instance.createdAt === "" ? "" : formatMoment(instance.createdAt, { locale })}
                  </td>
                  <td className={cn(CELL, "text-right")}>
                    {instance.spare || instance.status === "dropped" ? null : (
                      <DropDatabase contestId={contestId} instance={instance} dict={dict} />
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </ScrollBox>
      )}

      {databases.truncated ? (
        <p className="max-w-body text-small text-ink-2">{t.truncated}</p>
      ) : null}
    </section>
  );
}

/** The holder; a deleted account is shown as gone, never given an invented name. */
function Holder({
  instance,
  t,
}: {
  instance: GameInstance;
  t: Dictionary["workspace"]["databases"];
}) {
  if (instance.spare) return <Tag tone="mute">{t.spare}</Tag>;
  if (instance.participant === "") {
    return <span className="text-small text-ink-3">{t.formerParticipant}</span>;
  }
  return (
    <>
      <span className="block text-row text-ink">{instance.participantName}</span>
      <span className="block font-mono text-data text-ink-3">{instance.participant}</span>
    </>
  );
}

/** The confirmation names the participant, since the risk is pressing it on the wrong row. */
function DropDatabase({
  contestId,
  instance,
  dict,
}: {
  contestId: string;
  instance: GameInstance;
  dict: Dictionary;
}) {
  const t = dict.workspace.databases;
  const who = instance.participantName || instance.participant || instance.database;

  return (
    <ActionForm
      action={dropGameInstanceAction}
      fields={{ contestId, database: instance.database }}
      confirm={{ title: fill(t.confirm, { who: isolate(who) }), answer: t.drop, cancel: dict.chrome.cancel, danger: true }}
      errors={dict.errors}
      className="flex flex-col items-end gap-1"
    >
      <ActionButton size="sm" variant="quiet">
        {t.drop}
      </ActionButton>
      <ActionOutcome narrow />
    </ActionForm>
  );
}
