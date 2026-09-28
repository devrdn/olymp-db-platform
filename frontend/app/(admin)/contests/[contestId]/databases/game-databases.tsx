"use client";

import { useActionState } from "react";

import { Button } from "@/components/ui/button";
import { Tag } from "@/components/ui/tag";
import type { GameInstance, GameInstances } from "@/lib/api/game";
import { readableBytes } from "@/lib/format/bytes";
import { formatMoment } from "@/lib/format/datetime";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { dropGameInstanceAction, type GameState } from "./actions";
import { messageForCode } from "@/lib/i18n/errors";

const HEAD =
  "border-b border-line-2 px-(--row-px) py-2.5 font-mono text-label font-medium text-ink-3 uppercase";
const CELL = "border-b border-line px-(--row-px) py-(--row-py) align-middle";

const STATUS_TONE: Record<GameInstance["status"], "good" | "warn" | "bad" | "mute"> = {
  provisioning: "warn",
  ready: "good",
  failed: "bad",
  dropped: "mute",
};

/** A failure this panel is reporting, in the interface's own words. */
function message(code: string | undefined, dict: Dictionary): string | null {
  return code ? (messageForCode(code, dict.errors)) : null;
}

/**
 * The databases that already exist for one contest.
 *
 * Two kinds of row in one table rather than two tables: a spare copy and a
 * participant's own are one column apart in the record, and separating them on
 * screen would make "how many databases does this contest have" a sum the
 * reader has to do. What the table adds over a cluster listing is the holder's
 * name — the question this screen exists to answer is "which of these is
 * Ivan's", and a list of generated database names cannot answer it.
 *
 * Only a held copy offers the button. A spare that has gone wrong is not worth
 * an organiser's attention — nobody is waiting on it, and the pool tender
 * replaces it on its own, so a control duplicating a background job is how a
 * screen teaches somebody to distrust it. A row already dropped offers nothing
 * either, for the plainest of reasons.
 *
 * No heading of its own. This is the sole panel of the `databases` route —
 * the same reason `StoryEditor` and `QuestionList` carry none either — and
 * the route's own page supplies it, the way every other single-panel section
 * does.
 */
export function GameDatabases({
  contestId,
  databases,
  locale,
  dict,
}: {
  contestId: string;
  databases: GameInstances;
  locale: Locale;
  dict: Dictionary;
}) {
  const t = dict.workspace.databases;

  return (
    <section className="flex flex-col gap-5">
      {databases.instances.length === 0 ? (
        <p className="max-w-body text-body text-ink-2">{t.empty}</p>
      ) : (
        <div className="overflow-x-auto">
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
        </div>
      )}

      {databases.truncated ? (
        <p className="max-w-body text-small text-ink-2">{t.truncated}</p>
      ) : null}
    </section>
  );
}

/**
 * Whose copy this is.
 *
 * An account that has since been deleted is said to be gone rather than given
 * an invented name: the row outlives the person on it, which is the same rule
 * the audit trail keeps about the things it describes.
 */
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

/**
 * One row's button.
 *
 * The confirmation names the participant rather than asking "are you sure":
 * the mistake this guards against is pressing it on the wrong row, and a
 * question that does not say whose database it is cannot catch that.
 */
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
  const [state, formAction, pending] = useActionState<GameState, FormData>(
    dropGameInstanceAction,
    {},
  );
  const failure = message(state.code, dict);
  const who = instance.participantName || instance.participant || instance.database;

  return (
    <form
      action={formAction}
      className="flex flex-col items-end gap-1"
      onSubmit={(event) => {
        if (!window.confirm(t.confirm.replace("{who}", who))) event.preventDefault();
      }}
    >
      <input type="hidden" name="contestId" value={contestId} />
      <input type="hidden" name="database" value={instance.database} />

      <Button type="submit" size="sm" variant="quiet" disabled={pending}>
        {t.drop}
      </Button>

      {failure ? (
        <p role="alert" className="max-w-40 text-small text-balance text-bad">
          {failure}
        </p>
      ) : null}
    </form>
  );
}
