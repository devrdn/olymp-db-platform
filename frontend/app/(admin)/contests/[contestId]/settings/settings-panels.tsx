"use client";

import { useId, useState } from "react";

import { ActionFooter, ActionForm, useActionStatus } from "@/components/product/action-form";
import { ReloadButton } from "@/components/product/reload-button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Tag } from "@/components/ui/tag";
import { Tooltip } from "@/components/ui/tooltip";
import { type Contest, type ContestSummary } from "@/lib/api/contests";
import { ENROLLMENTS, PROGRESSIONS, QUESTION_MODES, SCORINGS, TIMINGS, VISIBILITIES } from "@/lib/api/contests-terms";
import { FREEZE_UNITS, freezeForForm } from "@/lib/api/leaderboard-terms";
import { type SqlPolicy } from "@/lib/api/policy";
import { SQL_MODES } from "@/lib/api/policy-terms";
import { wallClockFromInstant } from "@/lib/format/datetime";
import { LOCALES, LOCALE_NAMES, type Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { fill } from "@/lib/i18n/fill";
import { cn } from "@/lib/utils";

import {
  saveArchiveSettingsAction,
  saveLanguagesAction,
  savePolicyAction,
  savePrizePlacesAction,
  saveSettingsAction,
} from "./actions";

/**
 * A titled block with its own save. One form per endpoint, so one refusal
 * does not discard the others.
 */
export function Panel({
  title,
  help,
  dict,
  frozen,
  children,
}: {
  title: string;
  help?: string;
  dict: Dictionary;
  frozen?: string;
  children: React.ReactNode;
}) {
  return (
    <section className="flex flex-col gap-5 border-t border-line pt-5">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <div className="flex items-center gap-2">
          <h3 className="text-h3 text-ink">{title}</h3>
          {help ? <Tooltip label={dict.chrome.helpLabel}>{help}</Tooltip> : null}
        </div>
        {frozen ? <Tag tone="mute">{frozen}</Tag> : null}
      </div>
      {children}
    </section>
  );
}

/**
 * A legend with a "?" inside it: a disabled fieldset disables every button
 * except those in its first legend. The fieldset takes its name from the legend
 * text by id, so the button's name does not join it.
 */
function HelpLegend({
  id,
  children,
  help,
  dict,
}: {
  id: string;
  children: React.ReactNode;
  help: string;
  dict: Dictionary;
}) {
  return (
    <legend className="pb-2">
      <span className="inline-flex items-center gap-1.5">
        <span id={id} className="font-mono text-label text-ink-3 uppercase">
          {children}
        </span>
        <Tooltip label={dict.chrome.helpLabel}>{help}</Tooltip>
      </span>
    </legend>
  );
}

function Choices<T extends string>({
  name,
  values,
  labels,
  initial,
  disabled,
  onPick,
  unavailable,
}: {
  name: string;
  values: readonly T[];
  labels: Record<string, string>;
  initial: T;
  disabled?: boolean;
  onPick?: (value: T) => void;
  /** Values that cannot be chosen now, each with the id of the text saying why. */
  unavailable?: Partial<Record<T, string>>;
}) {
  const [picked, setPicked] = useState<T>(initial);

  return (
    <div className="flex flex-col gap-2.5">
      {values.map((value) => {
        const reasonId = unavailable?.[value];
        const off = disabled || reasonId !== undefined;
        return (
          <label
            key={value}
            className={cn(
              "flex items-center gap-3 text-control",
              off ? "cursor-not-allowed text-ink-3" : "cursor-pointer text-ink",
            )}
          >
            <input
              type="radio"
              name={name}
              value={value}
              checked={picked === value}
              disabled={off}
              aria-describedby={reasonId}
              onChange={() => {
                setPicked(value);
                onPick?.(value);
              }}
              className="size-4 cursor-pointer accent-cta disabled:cursor-not-allowed"
            />
            {labels[value]}
          </label>
        );
      })}
    </div>
  );
}

/**
 * Limits self-enrolment to verified addresses in the site's domain. Offered
 * where the site has a domain, or where the contest already holds the limit,
 * so it can be lifted after the domain is unset. It is absent only while off,
 * and an absent box is read as off, so every save still carries the value.
 */
function TrustedDomainSwitch({
  on,
  domain,
  editable,
  dict,
}: {
  on: boolean;
  /** The site's domain; "" when it has none, null when it could not say. */
  domain: string | null;
  editable: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.settings.access;
  const hintId = useId();
  const unsetId = useId();

  if (!on && !domain) return null;

  const label = domain ? fill(t.trustedDomainOnly, { domain }) : t.trustedDomainOnlyNoDomain;
  const unset = on && domain === "";

  return (
    <div className="flex flex-col gap-2">
      <label
        className={cn(
          "flex items-center gap-3 text-control",
          editable ? "cursor-pointer text-ink" : "cursor-not-allowed text-ink-3",
        )}
      >
        <input
          type="checkbox"
          name="trustedDomainOnly"
          defaultChecked={on}
          disabled={!editable}
          aria-describedby={unset ? `${hintId} ${unsetId}` : hintId}
          className="size-4 cursor-pointer accent-cta disabled:cursor-not-allowed"
        />
        {label}
      </label>
      <p id={hintId} className="max-w-body text-small text-ink-3">
        {t.trustedDomainOnlyHint}
      </p>
      {unset ? (
        <p id={unsetId} className="max-w-body text-small text-warn">
          {t.trustedDomainUnset}
        </p>
      ) : null}
    </div>
  );
}

type ContestPanelProps = {
  contest: Contest;
  editable: boolean;
  shapeOpen: boolean;
  /** The site's email domain from `/auth/methods`; "" for none, null when unknown. */
  trustedDomain?: string | null;
  dict: Dictionary;
};

/**
 * The contest's fields. While the contest runs settings stay editable but the
 * shape freezes; frozen controls stay in the form, disabled. A reload after a
 * save refused for a contest that moved starts the form over, so it shows the
 * contest as it is now and the refusal goes with what was typed.
 */
export function ContestPanel(props: ContestPanelProps) {
  const [reloads, setReloads] = useState(0);

  return <ContestForm key={reloads} {...props} onReload={() => setReloads((count) => count + 1)} />;
}

/** Offered under one refusal only: a save over a contest that moved since the form was rendered. */
function ReloadWhenChanged({ label, onReload }: { label: string; onReload: () => void }) {
  const { code } = useActionStatus();

  return code === "contest_changed" ? <ReloadButton label={label} onReload={onReload} /> : null;
}

function ContestForm({
  contest,
  editable,
  shapeOpen,
  trustedDomain = null,
  dict,
  onReload,
}: ContestPanelProps & { onReload: () => void }) {
  const t = dict.workspace.settings;
  const [timing, setTiming] = useState<string>(contest.timing);
  const [progression, setProgression] = useState<string>(contest.progression);
  const [scoring, setScoring] = useState<string>(contest.scoring);
  const orderLegendId = useId();
  const scoringLegendId = useId();
  const freezeLegendId = useId();
  const namesLegendId = useId();
  const resultDownloadHintId = useId();
  const schemaDiagramHintId = useId();
  const freezeInitial = freezeForForm(contest.leaderboard.freezeMin);
  const [freezeMode, setFreezeMode] = useState<string>(freezeInitial.mode);
  const [names, setNames] = useState<string>(contest.leaderboard.names);

  return (
    <ActionForm
      action={saveSettingsAction}
      fields={{ contestId: contest.id, expectedVersion: contest.version }}
      errors={dict.errors}
      revision={contest.updatedAt}
      className="contents"
    >
      <Panel title={t.schedule.heading} help={t.schedule.help} dict={dict}>
        <div className="grid gap-6 narrow:grid-cols-2">
          <Field id="startsAt" label={t.schedule.startsAt}>
            <Input
              name="startsAt"
              type="datetime-local"
              defaultValue={contest.startsAt ? wallClockFromInstant(contest.startsAt) : ""}
              disabled={!editable}
            />
          </Field>

          <Field
            id="endsAt"
            label={t.schedule.endsAt}
            hint={timing === "individual" ? t.schedule.endsAtIndividualHint : undefined}
          >
            <Input
              name="endsAt"
              type="datetime-local"
              defaultValue={contest.endsAt ? wallClockFromInstant(contest.endsAt) : ""}
              disabled={!editable}
            />
          </Field>

          <Field
            id="enrollmentDeadline"
            label={t.schedule.enrollmentDeadline}
            hint={t.schedule.enrollmentDeadlineHint}
          >
            <Input
              name="enrollmentDeadline"
              type="datetime-local"
              defaultValue={
                contest.settings.enrollmentDeadline
                  ? wallClockFromInstant(contest.settings.enrollmentDeadline)
                  : ""
              }
              disabled={!editable}
            />
          </Field>

          <Field
            id="gracePeriodMin"
            label={t.schedule.grace}
            help={t.schedule.graceHelp}
            helpLabel={dict.chrome.helpLabel}
          >
            <Input
              name="gracePeriodMin"
              type="number"
              min={0}
              step={1}
              defaultValue={contest.settings.gracePeriodMin}
              disabled={!editable}
            />
          </Field>
        </div>

        <p className="max-w-body text-small text-ink-3">{t.schedule.timezone}</p>
      </Panel>

      <Panel
        title={t.shape.heading}
        help={t.shape.help}
        dict={dict}
        frozen={shapeOpen ? undefined : dict.workspace.facts.frozen}
      >
        <fieldset className="flex flex-col gap-3" disabled={!shapeOpen}>
          <legend className="pb-2 font-mono text-label text-ink-3 uppercase">
            {t.shape.format}
          </legend>
          <Choices
            name="questionMode"
            values={QUESTION_MODES}
            labels={dict.contests.mode}
            initial={contest.questionMode}
            disabled={!shapeOpen}
          />
        </fieldset>

        <fieldset className="flex flex-col gap-3" disabled={!shapeOpen}>
          <legend className="pb-2 font-mono text-label text-ink-3 uppercase">
            {t.shape.timing}
          </legend>
          <Choices
            name="timing"
            values={TIMINGS}
            labels={dict.workspace.timing}
            initial={contest.timing}
            disabled={!shapeOpen}
            onPick={setTiming}
          />

          {timing === "individual" ? (
            <Field id="durationMin" label={t.shape.duration} hint={t.shape.durationHint}>
              <Input
                name="durationMin"
                type="number"
                min={1}
                step={1}
                defaultValue={contest.durationMin ?? 90}
                disabled={!shapeOpen}
                className="max-w-40"
              />
            </Field>
          ) : null}
        </fieldset>

        {/* Order and scoring freeze with the shape (docs/ARCHITECTURE.md
           §6.1.1); the rule must not change under a participant. */}
        <fieldset className="flex flex-col gap-3" disabled={!shapeOpen} aria-labelledby={orderLegendId}>
          <HelpLegend id={orderLegendId} help={t.shape.orderHelp} dict={dict}>
            {t.shape.order}
          </HelpLegend>
          <Choices
            name="progression"
            values={PROGRESSIONS}
            labels={dict.workspace.progression}
            initial={contest.progression}
            disabled={!shapeOpen}
            onPick={setProgression}
          />

          {/* The publish gate refuses this combination unless the scoring is
             ICPC (docs/ARCHITECTURE.md §6.1.1); say so here. */}
          {progression === "sequential" ? (
            scoring === "icpc" ? (
              <p className="max-w-body text-small text-ink-3">{t.shape.sequentialIcpcNote}</p>
            ) : (
              <p className="max-w-body text-small text-warn">{t.shape.sequentialWarning}</p>
            )
          ) : null}
        </fieldset>

        <fieldset className="flex flex-col gap-3" disabled={!shapeOpen} aria-labelledby={scoringLegendId}>
          <HelpLegend id={scoringLegendId} help={t.shape.scoringHelp} dict={dict}>
            {t.shape.scoring}
          </HelpLegend>
          <Choices
            name="scoring"
            values={SCORINGS}
            labels={dict.workspace.scoring}
            initial={contest.scoring}
            disabled={!shapeOpen}
            onPick={setScoring}
          />

          {/* ICPC penalty minutes per wrong attempt on a later-solved question
             (docs/ARCHITECTURE.md §6.1.1). Only in this mode; locked with the
             shape. */}
          {scoring === "icpc" ? (
            <Field id="icpcPenaltyMin" label={t.shape.icpcPenalty} hint={t.shape.icpcPenaltyHint}>
              <Input
                name="icpcPenaltyMin"
                type="number"
                min={0}
                max={240}
                step={1}
                defaultValue={contest.icpcPenaltyMin}
                disabled={!shapeOpen}
                className="max-w-40"
              />
            </Field>
          ) : null}
        </fieldset>
      </Panel>

      <Panel title={t.leaderboard.heading} help={t.leaderboard.help} dict={dict}>
        {/* Locked with the shape: moving the freeze mid-contest would briefly
           open the live table or hide one participants already saw. */}
        <fieldset className="flex flex-col gap-3" disabled={!shapeOpen} aria-labelledby={freezeLegendId}>
          <HelpLegend id={freezeLegendId} help={t.leaderboard.freezeHelp} dict={dict}>
            {t.leaderboard.freeze}
          </HelpLegend>
          <Choices
            name="leaderboardFreezeMode"
            values={["none", "before"] as const}
            labels={{ none: t.leaderboard.freezeNone, before: t.leaderboard.freezeBefore }}
            initial={freezeInitial.mode}
            disabled={!shapeOpen}
            onPick={setFreezeMode}
          />
          {freezeMode === "before" ? (
            <div className="flex flex-wrap items-end gap-6 pl-7">
              <Field id="leaderboardFreezeAmount" label={t.leaderboard.freezeAmount}>
                <Input
                  name="leaderboardFreezeAmount"
                  type="number"
                  min={1}
                  step={1}
                  required
                  defaultValue={freezeInitial.amount}
                  disabled={!shapeOpen}
                  className="max-w-28"
                />
              </Field>
              <Choices
                name="leaderboardFreezeUnit"
                values={FREEZE_UNITS}
                labels={{ minutes: t.leaderboard.unitMinutes, hours: t.leaderboard.unitHours }}
                initial={freezeInitial.unit}
                disabled={!shapeOpen}
              />
            </div>
          ) : null}
        </fieldset>

        <fieldset className="flex flex-col gap-3" disabled={!editable} aria-labelledby={namesLegendId}>
          <legend className="pb-2">
            <span id={namesLegendId} className="font-mono text-label text-ink-3 uppercase">
              {t.leaderboard.names}
            </span>
          </legend>
          <Choices
            name="leaderboardNames"
            values={["login", "full_name"] as const}
            labels={{ login: t.leaderboard.namesLogin, full_name: t.leaderboard.namesFullName }}
            initial={contest.leaderboard.names}
            disabled={!editable}
            onPick={setNames}
          />
          {/* The table is public. */}
          {names === "full_name" ? (
            <p className="max-w-body text-small text-warn">{t.leaderboard.namesPublicHint}</p>
          ) : null}
        </fieldset>
      </Panel>

      <Panel title={t.access.heading} help={t.access.help} dict={dict}>
        <fieldset className="flex flex-col gap-3" disabled={!editable}>
          <legend className="pb-2 font-mono text-label text-ink-3 uppercase">
            {t.access.enrollment}
          </legend>
          <Choices
            name="enrollment"
            values={ENROLLMENTS}
            labels={dict.contests.enrollment}
            initial={contest.enrollment}
            disabled={!editable}
          />
        </fieldset>

        <TrustedDomainSwitch
          on={contest.settings.trustedDomainOnly}
          domain={trustedDomain}
          editable={editable}
          dict={dict}
        />

        <Field id="allowedCidrs" label={t.access.network} hint={t.access.networkHint}>
          <Input
            name="allowedCidrs"
            defaultValue={contest.allowedCidrs.join(", ")}
            placeholder="10.24.0.0/16"
            disabled={!editable}
            className="font-mono text-data"
          />
        </Field>

        <Field id="queryRateLimitPerMin" label={t.access.rate} hint={t.access.rateHint}>
          <Input
            name="queryRateLimitPerMin"
            type="number"
            min={0}
            step={1}
            defaultValue={contest.settings.queryRateLimitPerMin}
            disabled={!editable}
            className="max-w-40"
          />
        </Field>

        <div className="flex flex-col gap-2">
          <label
            className={cn(
              "flex items-center gap-3 text-control",
              editable ? "cursor-pointer text-ink" : "cursor-not-allowed text-ink-3",
            )}
          >
            <input
              type="checkbox"
              name="resultDownload"
              defaultChecked={contest.resultDownload}
              disabled={!editable}
              aria-describedby={resultDownloadHintId}
              className="size-4 cursor-pointer accent-cta disabled:cursor-not-allowed"
            />
            {t.access.resultDownload}
          </label>
          <p id={resultDownloadHintId} className="max-w-body text-small text-ink-3">
            {t.access.resultDownloadHint}
          </p>
        </div>

        <div className="flex flex-col gap-2">
          <label
            className={cn(
              "flex items-center gap-3 text-control",
              editable ? "cursor-pointer text-ink" : "cursor-not-allowed text-ink-3",
            )}
          >
            <input
              type="checkbox"
              name="schemaDiagram"
              defaultChecked={contest.schemaDiagram}
              disabled={!editable}
              aria-describedby={schemaDiagramHintId}
              className="size-4 cursor-pointer accent-cta disabled:cursor-not-allowed"
            />
            {t.access.schemaDiagram}
          </label>
          <p id={schemaDiagramHintId} className="max-w-body text-small text-ink-3">
            {t.access.schemaDiagramHint}
          </p>
        </div>

        <ActionFooter labels={dict.workspace.settings} disabled={!editable}>
          <ReloadWhenChanged label={t.reload} onReload={onReload} />
        </ActionFooter>
      </Panel>
    </ActionForm>
  );
}

/**
 * How many places earn each prize. A form of its own, because it stays open
 * after the contest finishes, when the rest of the settings are closed.
 */
export function PrizePanel({
  contest,
  editable,
  dict,
}: {
  contest: Contest;
  editable: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.settings.prizes;
  const hintId = useId();
  const winnerId = useId();

  const fields = [
    { name: "prizeFirst", label: t.first, value: contest.prizePlaces.first },
    { name: "prizeSecond", label: t.second, value: contest.prizePlaces.second },
    { name: "prizeThird", label: t.third, value: contest.prizePlaces.third },
  ];

  // The counts, not updatedAt, which this save leaves alone.
  const { first, second, third } = contest.prizePlaces;
  return (
    <ActionForm
      action={savePrizePlacesAction}
      fields={{ contestId: contest.id }}
      errors={dict.errors}
      revision={`${first}-${second}-${third}`}
      className="contents"
    >
      <Panel
        title={t.heading}
        help={t.help}
        dict={dict}
        frozen={editable ? undefined : dict.workspace.facts.frozen}
      >
        <div className="grid gap-6 narrow:grid-cols-3">
          {fields.map((field) => (
            <Field key={field.name} id={field.name} label={field.label} describedBy={`${hintId} ${winnerId}`}>
              <Input
                name={field.name}
                type="number"
                min={0}
                max={1000}
                step={1}
                defaultValue={field.value}
                disabled={!editable}
                className="max-w-40"
              />
            </Field>
          ))}
        </div>

        <div className="flex flex-col gap-1">
          <p id={hintId} className="max-w-body text-small text-ink-3">
            {t.hint}
          </p>
          <p id={winnerId} className={cn("max-w-body text-small", contest.scoring === "winner" ? "text-warn" : "text-ink-3")}>
            {t.winnerHint}
          </p>
        </div>

        <ActionFooter labels={dict.workspace.settings} disabled={!editable} />
      </Panel>
    </ActionForm>
  );
}

/**
 * Whether the contest is listed once it ends, and whether the archive shows its
 * tasks. A form of its own, open in every status: the choice is made, and
 * changed, mostly after the contest has ended.
 */
export function ArchivePanel({
  contest,
  archiveOn,
  dict,
}: {
  contest: Contest;
  /** The installation switch; off, the choices are kept but nothing is listed. */
  archiveOn: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.settings.archive;
  const reasonId = useId();
  const tasksHintId = useId();

  // Self-registration lists the contest to anyone, so it cannot be closed; the
  // server refuses the same combination.
  const selfRegistration = contest.enrollment === "open";

  // The choices, not updatedAt, which this save leaves alone.
  return (
    <ActionForm
      action={saveArchiveSettingsAction}
      fields={{ contestId: contest.id }}
      errors={dict.errors}
      revision={`${contest.visibility}-${contest.archiveTasks}`}
      className="contents"
    >
      <Panel title={t.heading} help={t.help} dict={dict}>
        {archiveOn ? null : (
          <p className="max-w-body text-small text-warn">{t.switchedOff}</p>
        )}

        <fieldset className="flex flex-col gap-3">
          <legend className="pb-2 font-mono text-label text-ink-3 uppercase">{t.visibility}</legend>
          <Choices
            name="visibility"
            values={VISIBILITIES}
            labels={{ open: t.visibilityOpen, closed: t.visibilityClosed }}
            initial={contest.visibility}
            unavailable={selfRegistration ? { closed: reasonId } : undefined}
          />
          {selfRegistration ? (
            <p id={reasonId} className="max-w-body text-small text-ink-3">
              {t.selfRegistrationOpen}
            </p>
          ) : null}
        </fieldset>

        <div className="flex flex-col gap-2">
          <label className="flex cursor-pointer items-center gap-3 text-control text-ink">
            <input
              type="checkbox"
              name="archiveTasks"
              defaultChecked={contest.archiveTasks}
              aria-describedby={tasksHintId}
              className="size-4 cursor-pointer accent-cta"
            />
            {t.tasks}
          </label>
          <p id={tasksHintId} className="max-w-body text-small text-ink-3">
            {t.tasksHint}
          </p>
        </div>

        <ActionFooter labels={dict.workspace.settings} />
      </Panel>
    </ActionForm>
  );
}

/** The contest's languages. Dropping one drops its title, story and question texts. */
export function LanguagePanel({
  contest,
  editable,
  dict,
}: {
  contest: Contest;
  editable: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.settings;
  const declared = contest.languages.map((l) => l.code);
  const fallbackCode = contest.languages.find((l) => l.isDefault)?.code ?? declared[0];

  const [chosen, setChosen] = useState<string[]>(declared);
  const [fallback, setFallback] = useState<string>(fallbackCode ?? "en");

  function toggle(code: Locale, on: boolean) {
    setChosen((current) => {
      const next = on ? [...current, code] : current.filter((c) => c !== code);
      if (!next.includes(fallback) && next[0]) setFallback(next[0]);
      return next;
    });
  }

  return (
    <ActionForm action={saveLanguagesAction} fields={{ contestId: contest.id }} errors={dict.errors} className="contents">
      <Panel title={t.languages.heading} help={t.languages.help} dict={dict}>
        <div className="flex flex-col gap-2.5">
          {LOCALES.map((code) => {
            const on = chosen.includes(code);

            return (
              <div key={code} className="flex items-center gap-3">
                <input
                  type="checkbox"
                  id={`lang-${code}`}
                  name="languages"
                  value={code}
                  checked={on}
                  disabled={!editable}
                  onChange={(event) => toggle(code, event.target.checked)}
                  className="size-4 cursor-pointer accent-cta disabled:cursor-not-allowed"
                />
                <label htmlFor={`lang-${code}`} className="cursor-pointer text-control text-ink">
                  {LOCALE_NAMES[code]}
                  <span className="ml-2 font-mono text-data text-ink-3 uppercase">{code}</span>
                </label>

                <label
                  className={cn(
                    "ml-auto flex items-center gap-2 text-small",
                    on && editable ? "cursor-pointer text-ink-2" : "text-ink-3 opacity-45",
                  )}
                >
                  <input
                    type="radio"
                    name="defaultLanguage"
                    value={code}
                    checked={fallback === code}
                    disabled={!on || !editable}
                    onChange={() => setFallback(code)}
                    className="size-3.5 cursor-pointer accent-cta disabled:cursor-not-allowed"
                  />
                  {t.languages.fallback}
                </label>
              </div>
            );
          })}
        </div>

        <p className="max-w-body text-small text-ink-3">{t.languages.warning}</p>

        <ActionFooter labels={dict.workspace.settings} disabled={!editable} />
      </Panel>
    </ActionForm>
  );
}

/**
 * What participants may do to their copy of the game database. Freezes with the
 * content: a mid-contest change would grant different rights by connection
 * time.
 */
export function PolicyPanel({
  contest,
  policy,
  editable,
  dict,
}: {
  contest: ContestSummary | Contest;
  policy: SqlPolicy;
  editable: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.settings;
  const [mode, setMode] = useState<string>(policy.mode);

  const toggles = [
    { name: "allowCreateView", on: policy.allowCreateView, label: t.policy.createView },
    { name: "allowOwnTables", on: policy.allowOwnTables, label: t.policy.ownTables },
    { name: "allowTempTables", on: policy.allowTempTables, label: t.policy.tempTables },
    { name: "allowCatalog", on: policy.allowCatalog, label: t.policy.catalog },
  ];

  // The policy has its own stamp.
  return (
    <ActionForm
      action={savePolicyAction}
      fields={{ contestId: contest.id }}
      errors={dict.errors}
      revision={policy.updatedAt}
      className="contents"
    >
      {(state) => (
        <Panel
          title={t.policy.heading}
          help={t.policy.help}
          dict={dict}
          frozen={editable ? undefined : dict.workspace.facts.frozen}
        >
          <fieldset className="flex flex-col gap-3" disabled={!editable}>
            <legend className="pb-2 font-mono text-label text-ink-3 uppercase">
              {t.policy.mode}
            </legend>
            <Choices
              name="mode"
              values={SQL_MODES}
              labels={t.policy.modes}
              initial={policy.mode}
              disabled={!editable}
              onPick={setMode}
            />
          </fieldset>

          {/* Only read-write grants writable tables. */}
          {mode === "read_write" ? (
            <Field
              id="writableTables"
              label={t.policy.tables}
              hint={t.policy.tablesHint}
              help={t.policy.tablesHelp}
              helpLabel={dict.chrome.helpLabel}
            >
              <Input
                name="writableTables"
                defaultValue={policy.writableTables.join(", ")}
                disabled={!editable}
                className="font-mono text-data"
              />
            </Field>
          ) : null}

          <div className="flex flex-col gap-2.5">
            {toggles.map((toggle) => (
              <label
                key={toggle.name}
                className={cn(
                  "flex items-center gap-3 text-control",
                  editable ? "cursor-pointer text-ink" : "cursor-not-allowed text-ink-3",
                )}
              >
                <input
                  type="checkbox"
                  name={toggle.name}
                  defaultChecked={toggle.on}
                  disabled={!editable}
                  className="size-4 cursor-pointer accent-cta disabled:cursor-not-allowed"
                />
                {toggle.label}
              </label>
            ))}
          </div>

          <Field id="diskQuotaRatio" label={t.policy.quota} hint={t.policy.quotaHint}>
            <Input
              name="diskQuotaRatio"
              type="number"
              min={1}
              step={1}
              defaultValue={policy.diskQuotaRatio}
              disabled={!editable}
              className="max-w-40"
            />
          </Field>

          <ActionFooter labels={dict.workspace.settings} disabled={!editable}>
            {/* The server answers one code; only this side knows which entries were wrong. */}
            {state.rejected && state.rejected.length > 0 ? (
              <p className="max-w-body font-mono text-data text-warn">{state.rejected.join(", ")}</p>
            ) : null}
          </ActionFooter>
        </Panel>
      )}
    </ActionForm>
  );
}
