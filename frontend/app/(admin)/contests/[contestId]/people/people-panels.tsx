"use client";

import { useActionState } from "react";

import { StateView } from "@/components/product/state-view";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Tag } from "@/components/ui/tag";
import { Textarea } from "@/components/ui/textarea";
import { removable } from "@/lib/api/people-terms";
import { type Manager, type Participant, type RegistrationStatus } from "@/lib/api/people";
import { formatMoment } from "@/lib/format/datetime";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import {
  disqualifyParticipantAction,
  grantManagerAction,
  importParticipantsAction,
  removeParticipantAction,
  revokeManagerAction,
  type ImportState,
  type PeopleState,
} from "./actions";

const HEAD =
  "border-b border-line-2 px-(--row-px) py-2.5 font-mono text-label font-medium text-ink-3 uppercase";
const CELL = "border-b border-line px-(--row-px) py-(--row-py) align-middle";

const STATUS_TONE: Record<RegistrationStatus, "live" | "good" | "mute" | "bad"> = {
  registered: "mute",
  active: "live",
  finished: "good",
  disqualified: "bad",
};

/** A failure this panel is reporting, in the interface's own words. */
function message(code: string | undefined, dict: Dictionary): string | null {
  return code ? ((dict.errors as Record<string, string>)[code] ?? dict.errors.fallback) : null;
}

/**
 * Who runs the contest.
 *
 * The owner is in the list and has no controls, deliberately: ownership is not
 * granted or revoked here. Two owners make "who may appoint" ambiguous and
 * none leaves the contest with nobody who can appoint anyone, so handing a
 * contest over is a separate act rather than a quiet consequence of editing a
 * list. A row that cannot be acted on still belongs in the list — leaving the
 * owner out would make the staff list wrong.
 */
export function ManagerPanel({
  contestId,
  managers,
  dict,
}: {
  contestId: string;
  managers: Manager[];
  dict: Dictionary;
}) {
  const t = dict.workspace.people;

  return (
    <section className="flex flex-col gap-5">
      <div className="flex flex-col gap-1.5">
        <h3 className="text-h3 text-ink">{t.managers.heading}</h3>
        <p className="max-w-body text-small text-ink-2">{t.managers.hint}</p>
      </div>

      <div className="overflow-x-auto">
        <table className="w-full min-w-lg border-collapse text-left">
          <thead>
            <tr>
              <th scope="col" className={HEAD}>
                {t.columns.person}
              </th>
              <th scope="col" className={cn(HEAD, "w-32")}>
                {t.columns.role}
              </th>
              <th scope="col" className={cn(HEAD, "w-24")}>
                <span className="sr-only">{t.managers.revoke}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {managers.map((manager) => (
              <tr key={manager.userId}>
                <td className={CELL}>
                  <span className="block text-row text-ink">{manager.fullName}</span>
                  <span className="block font-mono text-data text-ink-3">{manager.login}</span>
                </td>
                <td className={CELL}>
                  <Tag tone={manager.role === "owner" ? "ink" : "mute"}>
                    {t.role[manager.role]}
                  </Tag>
                </td>
                <td className={cn(CELL, "text-right")}>
                  {manager.role === "owner" ? null : (
                    <RowAction
                      action={revokeManagerAction}
                      contestId={contestId}
                      userId={manager.userId}
                      label={t.managers.revoke}
                      dict={dict}
                    />
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <GrantManager contestId={contestId} dict={dict} />
    </section>
  );
}

function GrantManager({ contestId, dict }: { contestId: string; dict: Dictionary }) {
  const t = dict.workspace.people;
  const [state, formAction, pending] = useActionState<PeopleState, FormData>(
    grantManagerAction,
    {},
  );
  const failure = message(state.code, dict);

  return (
    <form action={formAction} className="flex flex-col gap-3">
      <input type="hidden" name="contestId" value={contestId} />

      <div className="flex flex-wrap items-end gap-3">
        <div className="flex flex-col gap-2">
          <label htmlFor="managerId" className="font-mono text-label text-ink-3 uppercase">
            {t.managers.add}
          </label>
          {/* An identifier, not a login: the API grants by user id and this
              build has no directory to search. The users screen is where an
              administrator copies one from, and saying so beats a field that
              silently wants something the author does not have. */}
          <Input id="managerId" name="userId" placeholder={t.managers.idPlaceholder} className="w-96 max-w-full" />
        </div>

        <Button type="submit" variant="secondary" disabled={pending}>
          {pending ? t.managers.adding : t.managers.addAction}
        </Button>
      </div>

      <p className="max-w-body text-small text-ink-3">{t.managers.idHint}</p>

      {failure ? (
        <p role="alert" className="max-w-body text-small text-bad">
          {failure}
        </p>
      ) : null}
    </form>
  );
}

/**
 * Who takes part.
 *
 * Two different controls on a row, and which one appears is the whole point:
 * somebody who has not started can be removed, and somebody who has can only
 * be disqualified, because their queries and answers are part of the record of
 * the contest.
 */
export function ParticipantPanel({
  contestId,
  participants,
  total,
  locale,
  dict,
}: {
  contestId: string;
  participants: Participant[];
  total: number;
  locale: Locale;
  dict: Dictionary;
}) {
  const t = dict.workspace.people;

  return (
    <section className="flex flex-col gap-5">
      <div className="flex flex-wrap items-baseline justify-between gap-x-6 gap-y-1.5">
        <div className="flex flex-col gap-1.5">
          <h3 className="text-h3 text-ink">{t.participants.heading}</h3>
          <p className="max-w-body text-small text-ink-2">{t.participants.hint}</p>
        </div>
        <span className="font-mono text-data text-ink-3">
          {total} {t.participants.countLabel}
        </span>
      </div>

      {participants.length === 0 ? (
        <div className="border-t border-line">
          <StateView
            state={{
              kind: "empty",
              title: t.participants.empty.title,
              body: t.participants.empty.body,
            }}
          />
        </div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full min-w-2xl border-collapse text-left">
            <thead>
              <tr>
                <th scope="col" className={HEAD}>
                  {t.columns.person}
                </th>
                <th scope="col" className={cn(HEAD, "w-36")}>
                  {t.columns.state}
                </th>
                <th scope="col" className={cn(HEAD, "w-44")}>
                  {t.columns.started}
                </th>
                <th scope="col" className={cn(HEAD, "w-20 text-right")}>
                  {t.columns.score}
                </th>
                <th scope="col" className={cn(HEAD, "w-36")}>
                  <span className="sr-only">{t.participants.remove}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {participants.map((participant) => (
                <tr key={participant.registrationId}>
                  <td className={CELL}>
                    <span className="block text-row text-ink">{participant.fullName}</span>
                    <span className="block font-mono text-data text-ink-3">
                      {participant.login}
                    </span>
                  </td>
                  <td className={CELL}>
                    <Tag tone={STATUS_TONE[participant.status]}>
                      {t.registration[participant.status]}
                    </Tag>
                  </td>
                  <td className={cn(CELL, "font-mono text-data text-ink-2")}>
                    {participant.startedAt ? (
                      formatMoment(participant.startedAt, { locale })
                    ) : (
                      <span className="text-ink-3">{t.participants.notStarted}</span>
                    )}
                  </td>
                  <td className={cn(CELL, "text-right font-mono text-data text-ink-2")}>
                    {participant.totalScore}
                  </td>
                  <td className={cn(CELL, "text-right")}>
                    {removable(participant) ? (
                      <RowAction
                        action={removeParticipantAction}
                        contestId={contestId}
                        userId={participant.userId}
                        label={t.participants.remove}
                        dict={dict}
                      />
                    ) : participant.status === "disqualified" ? null : (
                      <RowAction
                        action={disqualifyParticipantAction}
                        contestId={contestId}
                        userId={participant.userId}
                        label={t.participants.disqualify}
                        confirm={t.participants.confirmDisqualify}
                        dict={dict}
                      />
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <ImportParticipants contestId={contestId} dict={dict} />
    </section>
  );
}

/** One row's one control, with its own pending state and its own failure. */
function RowAction({
  action,
  contestId,
  userId,
  label,
  confirm,
  dict,
}: {
  action: (previous: PeopleState, form: FormData) => Promise<PeopleState>;
  contestId: string;
  userId: string;
  label: string;
  confirm?: string;
  dict: Dictionary;
}) {
  const [state, formAction, pending] = useActionState<PeopleState, FormData>(action, {});
  const failure = message(state.code, dict);

  return (
    <form
      action={formAction}
      className="flex flex-col items-end gap-1"
      onSubmit={(event) => {
        if (confirm && !window.confirm(confirm)) event.preventDefault();
      }}
    >
      <input type="hidden" name="contestId" value={contestId} />
      <input type="hidden" name="userId" value={userId} />

      <Button type="submit" size="sm" variant="quiet" disabled={pending}>
        {label}
      </Button>

      {failure ? (
        <p role="alert" className="max-w-40 text-small text-balance text-bad">
          {failure}
        </p>
      ) : null}
    </form>
  );
}

function ImportParticipants({ contestId, dict }: { contestId: string; dict: Dictionary }) {
  const t = dict.workspace.people;
  const [state, formAction, pending] = useActionState<ImportState, FormData>(
    importParticipantsAction,
    {},
  );
  const failure = message(state.code, dict);

  return (
    <form action={formAction} className="flex flex-col gap-3 border-t border-line pt-5">
      <input type="hidden" name="contestId" value={contestId} />

      <label htmlFor="logins" className="font-mono text-label text-ink-3 uppercase">
        {t.import.heading}
      </label>
      <p className="max-w-body text-small text-ink-2">{t.import.hint}</p>

      <Textarea
        id="logins"
        name="logins"
        className="min-h-28 max-w-96 font-mono text-data"
        placeholder={t.import.placeholder}
      />

      <Button type="submit" variant="secondary" disabled={pending} className="self-start">
        {pending ? t.import.importing : t.import.action}
      </Button>

      {/* A partial success is the honest answer, so it is reported as one: the
          count that went in, and every line that did not with its reason. A
          revalidated list can show the first and never the second. */}
      {state.result ? (
        <div role="status" className="flex flex-col gap-2 pt-1">
          <p className="text-small text-good">
            {t.import.added.replace("{n}", String(state.result.added))}
          </p>

          {state.result.skipped.length > 0 ? (
            <ul className="flex flex-col gap-1">
              {state.result.skipped.map((row) => (
                <li key={row.ref} className="font-mono text-data text-ink-2">
                  {row.ref}
                  <span className="ml-3 text-warn">
                    {(t.import.reason as Record<string, string>)[row.reason] ?? row.reason}
                  </span>
                </li>
              ))}
            </ul>
          ) : null}
        </div>
      ) : null}

      {failure ? (
        <p role="alert" className="max-w-body text-small text-bad">
          {failure}
        </p>
      ) : null}
    </form>
  );
}
