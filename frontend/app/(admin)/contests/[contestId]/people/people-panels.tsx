"use client";

import { ActionButton, ActionForm, ActionOutcome } from "@/components/product/action-form";
import { StateView } from "@/components/product/state-view";
import { ScrollBox } from "@/components/ui/scroll-box";
import { Tag } from "@/components/ui/tag";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip } from "@/components/ui/tooltip";
import type { Question } from "@/components/ui/use-confirm";
import type { Scoring } from "@/lib/api/contests";
import type { Manager, Participant } from "@/lib/api/people";
import { removable, type RegistrationStatus } from "@/lib/api/people-terms";
import { formatMoment } from "@/lib/format/datetime";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { fill, isolate } from "@/lib/i18n/fill";
import { labelFor } from "@/lib/i18n/label";
import { cn } from "@/lib/utils";

import {
  addParticipantAction,
  disqualifyParticipantAction,
  grantManagerAction,
  importParticipantsAction,
  removeParticipantAction,
  revokeManagerAction,
  type PeopleState,
} from "./actions";
import { PersonPicker } from "./person-picker";

const HEAD =
  "border-b border-line-2 px-(--row-px) py-2.5 font-mono text-label font-medium text-ink-3 uppercase";
const CELL = "border-b border-line px-(--row-px) py-(--row-py) align-middle";

const STATUS_TONE: Record<RegistrationStatus, "live" | "good" | "mute" | "bad"> = {
  registered: "mute",
  active: "live",
  finished: "good",
  disqualified: "bad",
};

/**
 * Who runs the contest. The owner is listed without controls: ownership is not
 * granted or revoked here, since two owners make appointing ambiguous and none
 * leaves nobody to appoint.
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
      <div className="flex items-center gap-2">
        <h3 className="text-h3 text-ink">{t.managers.heading}</h3>
        <Tooltip label={dict.chrome.helpLabel}>{t.managers.help}</Tooltip>
      </div>

      <ScrollBox>
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
      </ScrollBox>

      <GrantManager contestId={contestId} dict={dict} />
    </section>
  );
}

function GrantManager({ contestId, dict }: { contestId: string; dict: Dictionary }) {
  const t = dict.workspace.people;

  return (
    <ActionForm action={grantManagerAction} fields={{ contestId }} errors={dict.errors} className="flex flex-col gap-3">
      <div className="flex flex-wrap items-end gap-3">
        <PersonPicker
          id="managerId"
          name="userId"
          contestId={contestId}
          label={t.managers.add}
          placeholder={t.picker.placeholder}
          helpText={t.picker.helpText}
          searchingText={t.picker.searching}
          noResultsText={t.picker.noResults}
          searchFailedText={t.picker.searchFailed}
          changeText={t.picker.change}
          selectedTemplate={t.picker.selected}
        />

        <ActionButton variant="secondary" pendingLabel={t.managers.adding}>
          {t.managers.addAction}
        </ActionButton>
      </div>

      <ActionOutcome />
    </ActionForm>
  );
}

/**
 * Who takes part. Someone who has not started can be removed; someone who has
 * can only be disqualified, since their work is part of the record. Once the
 * contest has ended a removal asks first, since the roster is then part of
 * the record too.
 */
export function ParticipantPanel({
  contestId,
  participants,
  total,
  locale,
  scoring,
  ended,
  trustedDomain = "",
  trustedDomainOnly = false,
  dict,
}: {
  contestId: string;
  participants: Participant[];
  total: number;
  locale: Locale;
  /** The site's email domain from `/auth/methods`; "" when it has none or could not say. */
  trustedDomain?: string;
  /**
   * The contest admits only that domain to self-enrolment. Only then are the
   * exceptions marked: in an open contest most participants would be.
   */
  trustedDomainOnly?: boolean;
  // ICPC ranks by solved count, then penalty (docs/ARCHITECTURE.md §6.1.1);
  // `total_score` is always 0 there, so its column is hidden.
  scoring: Scoring;
  /** The contest is finished or archived. */
  ended: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.people;
  const showScore = scoring !== "icpc";
  const untrusted =
    trustedDomainOnly && trustedDomain
      ? fill(t.participants.untrustedEmail, { domain: trustedDomain })
      : null;

  return (
    <section className="flex flex-col gap-5">
      <div className="flex flex-wrap items-baseline justify-between gap-x-6 gap-y-1.5">
        <div className="flex items-center gap-2">
          <h3 className="text-h3 text-ink">{t.participants.heading}</h3>
          <Tooltip label={dict.chrome.helpLabel}>{t.participants.help}</Tooltip>
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
        <ScrollBox>
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
                {showScore ? (
                  <th scope="col" className={cn(HEAD, "w-20 text-right")}>
                    {t.columns.score}
                  </th>
                ) : null}
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
                    {untrusted && participant.trustedEmail === false ? (
                      <span className="mt-1 block text-small text-warn">{untrusted}</span>
                    ) : null}
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
                  {showScore ? (
                    <td className={cn(CELL, "text-right font-mono text-data text-ink-2")}>
                      {participant.totalScore}
                    </td>
                  ) : null}
                  <td className={cn(CELL, "text-right")}>
                    {removable(participant) ? (
                      <RowAction
                        action={removeParticipantAction}
                        contestId={contestId}
                        userId={participant.userId}
                        label={t.participants.remove}
                        confirm={
                          ended
                            ? {
                                title: fill(t.participants.confirmRemoveEnded, {
                                  name: isolate(participant.fullName || participant.login),
                                }),
                                detail: t.participants.confirmRemoveEndedBody,
                                answer: t.participants.remove,
                                cancel: dict.chrome.cancel,
                                danger: true,
                              }
                            : undefined
                        }
                        dict={dict}
                      />
                    ) : participant.status === "disqualified" ? null : (
                      <RowAction
                        action={disqualifyParticipantAction}
                        contestId={contestId}
                        userId={participant.userId}
                        label={t.participants.disqualify}
                        confirm={{
                          title: t.participants.confirmDisqualify,
                          answer: t.participants.disqualify,
                          cancel: dict.chrome.cancel,
                          danger: true,
                        }}
                        dict={dict}
                      />
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </ScrollBox>
      )}

      <AddOneParticipant contestId={contestId} dict={dict} />
      <ImportParticipants contestId={contestId} dict={dict} />
    </section>
  );
}

/** One row's control, with its own pending state and failure. */
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
  confirm?: Question;
  dict: Dictionary;
}) {
  return (
    <ActionForm
      action={action}
      fields={{ contestId, userId }}
      confirm={confirm}
      errors={dict.errors}
      className="flex flex-col items-end gap-1"
    >
      <ActionButton size="sm" variant="quiet">
        {label}
      </ActionButton>
      <ActionOutcome narrow />
    </ActionForm>
  );
}

/**
 * Adds one person found by search; the roster import below is for a whole
 * cohort. Both end at `POST /participants`.
 */
function AddOneParticipant({ contestId, dict }: { contestId: string; dict: Dictionary }) {
  const t = dict.workspace.people;

  return (
    <ActionForm
      action={addParticipantAction}
      fields={{ contestId }}
      errors={dict.errors}
      className="flex flex-col gap-3 border-t border-line pt-5"
    >
      {(state) => (
        <>
          {/* No heading: the picker's label already says it. */}
          <div className="flex flex-wrap items-end gap-3">
            <PersonPicker
              id="participantId"
              name="userId"
              contestId={contestId}
              label={t.addOne.heading}
              placeholder={t.picker.placeholder}
              helpText={t.picker.helpText}
              searchingText={t.picker.searching}
              noResultsText={t.picker.noResults}
              searchFailedText={t.picker.searchFailed}
              changeText={t.picker.change}
              selectedTemplate={t.picker.selected}
              help={t.addOne.help}
              helpLabel={dict.chrome.helpLabel}
            />

            <ActionButton variant="secondary" pendingLabel={t.addOne.adding}>
              {t.addOne.action}
            </ActionButton>
          </div>

          {/* Not an API error: the server answered 200 and skipped the entry, so
             read it through the import's reason vocabulary. */}
          {state.skipReason ? (
            <p role="alert" className="max-w-body text-small text-warn">
              {labelFor(t.import.reason, state.skipReason) ?? state.skipReason}
            </p>
          ) : null}

          <ActionOutcome />
        </>
      )}
    </ActionForm>
  );
}

function ImportParticipants({ contestId, dict }: { contestId: string; dict: Dictionary }) {
  const t = dict.workspace.people;

  return (
    <ActionForm
      action={importParticipantsAction}
      fields={{ contestId }}
      errors={dict.errors}
      className="flex flex-col gap-3 border-t border-line pt-5"
    >
      {(state) => (
        <>
          <div className="flex items-center gap-1.5">
            <label htmlFor="logins" className="font-mono text-label text-ink-3 uppercase">
              {t.import.heading}
            </label>
            <Tooltip label={dict.chrome.helpLabel}>{t.import.help}</Tooltip>
          </div>
          {/* The format stays on screen as the textarea's description. */}
          <p id="logins-format" className="max-w-body text-small text-ink-2">
            {t.import.hint}
          </p>

          <Textarea
            id="logins"
            name="logins"
            aria-describedby="logins-format"
            className="min-h-28 max-w-96 font-mono text-data"
            placeholder={t.import.placeholder}
          />

          <ActionButton variant="secondary" pendingLabel={t.import.importing} className="self-start">
            {t.import.action}
          </ActionButton>

          {/* Report a partial success: the count added and every rejected line with
             its reason, which a revalidated list cannot show. */}
          {state.result ? (
            <div role="status" className="flex flex-col gap-2 pt-1">
              <p className="text-small text-good">
                {fill(t.import.added, { n: state.result.added })}
              </p>

              {state.result.skipped.length > 0 ? (
                <ul className="flex flex-col gap-1">
                  {state.result.skipped.map((row) => (
                    <li key={row.ref} className="font-mono text-data text-ink-2">
                      {row.ref}
                      <span className="ml-3 text-warn">
                        {labelFor(t.import.reason, row.reason) ?? row.reason}
                      </span>
                    </li>
                  ))}
                </ul>
              ) : null}
            </div>
          ) : null}

          <ActionOutcome />
        </>
      )}
    </ActionForm>
  );
}
