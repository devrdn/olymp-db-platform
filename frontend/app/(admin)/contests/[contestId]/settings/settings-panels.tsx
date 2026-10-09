"use client";

import { useActionState, useId, useState } from "react";

import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Tag } from "@/components/ui/tag";
import { Tooltip } from "@/components/ui/tooltip";
import { ENROLLMENTS, PROGRESSIONS, QUESTION_MODES, SCORINGS, TIMINGS } from "@/lib/api/contests-terms";
import { type Contest, type ContestSummary } from "@/lib/api/contests";
import { FREEZE_UNITS, freezeForForm } from "@/lib/api/leaderboard";
import { SQL_MODES } from "@/lib/api/policy-terms";
import { type SqlPolicy } from "@/lib/api/policy";
import { wallClockFromInstant } from "@/lib/format/datetime";
import { LOCALES, LOCALE_NAMES, type Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import {
  saveLanguagesAction,
  savePolicyAction,
  saveSettingsAction,
  type SettingsState,
} from "./actions";
import { messageForCode } from "@/lib/i18n/errors";

/**
 * A titled block with its own save. Four forms for four endpoints, so one
 * refusal does not discard the others.
 */
function Panel({
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

function SaveRow({
  state,
  pending,
  editable,
  dict,
}: {
  state: SettingsState;
  pending: boolean;
  editable: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.settings;

  const failure = state.code
    ? (messageForCode(state.code, dict.errors))
    : null;

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-4">
        <Button type="submit" disabled={pending || !editable}>
          {pending ? t.saving : t.save}
        </Button>

        {state.saved && !failure ? (
          <p role="status" className="text-small text-good">
            {t.saved}
          </p>
        ) : null}

        {failure ? (
          <p role="alert" className="max-w-body text-small text-bad">
            {failure}
          </p>
        ) : null}
      </div>

      {/* The server answers one code; only this side knows which entries were wrong. */}
      {state.rejected && state.rejected.length > 0 ? (
        <p className="max-w-body font-mono text-data text-warn">{state.rejected.join(", ")}</p>
      ) : null}
    </div>
  );
}

function Choices<T extends string>({
  name,
  values,
  labels,
  initial,
  disabled,
  onPick,
}: {
  name: string;
  values: readonly T[];
  labels: Record<string, string>;
  initial: T;
  disabled?: boolean;
  onPick?: (value: T) => void;
}) {
  const [picked, setPicked] = useState<T>(initial);

  return (
    <div className="flex flex-col gap-2.5">
      {values.map((value) => (
        <label
          key={value}
          className={cn(
            "flex items-center gap-3 text-control",
            disabled ? "cursor-not-allowed text-ink-3" : "cursor-pointer text-ink",
          )}
        >
          <input
            type="radio"
            name={name}
            value={value}
            checked={picked === value}
            disabled={disabled}
            onChange={() => {
              setPicked(value);
              onPick?.(value);
            }}
            className="size-4 cursor-pointer accent-cta disabled:cursor-not-allowed"
          />
          {labels[value]}
        </label>
      ))}
    </div>
  );
}

/**
 * The contest's fields. While the contest runs settings stay editable but the
 * shape freezes; frozen controls stay in the form, disabled.
 */
export function ContestPanel({
  contest,
  editable,
  shapeOpen,
  dict,
}: {
  contest: Contest;
  editable: boolean;
  shapeOpen: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.settings;
  const [state, formAction, pending] = useActionState<SettingsState, FormData>(
    saveSettingsAction,
    {},
  );
  const [timing, setTiming] = useState<string>(contest.timing);
  const [progression, setProgression] = useState<string>(contest.progression);
  const [scoring, setScoring] = useState<string>(contest.scoring);
  const orderLegendId = useId();
  const scoringLegendId = useId();
  const freezeLegendId = useId();
  const namesLegendId = useId();
  const freezeInitial = freezeForForm(contest.leaderboard.freezeMin);
  const [freezeMode, setFreezeMode] = useState<string>(freezeInitial.mode);
  const [names, setNames] = useState<string>(contest.leaderboard.names);

  // Keyed by the server's version: the fields are uncontrolled, so only a
  // remount shows the saved values. The key is on the form, not the panel, so
  // `useActionState` and the "saved" message survive, and it changes only when
  // the server copy does.
  return (
    <form key={contest.updatedAt} action={formAction} className="contents">
      <Panel title={t.schedule.heading} help={t.schedule.help} dict={dict}>
        <input type="hidden" name="contestId" value={contest.id} />

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

          {/* The publish gate refuses this combination
             (docs/ARCHITECTURE.md §6.1.1); say so here. */}
          {progression === "sequential" ? (
            <p className="max-w-body text-small text-warn">{t.shape.sequentialWarning}</p>
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

        <SaveRow state={state} pending={pending} editable={editable} dict={dict} />
      </Panel>
    </form>
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

  const [state, formAction, pending] = useActionState<SettingsState, FormData>(
    saveLanguagesAction,
    {},
  );
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
    <form action={formAction} className="contents">
      <Panel title={t.languages.heading} help={t.languages.help} dict={dict}>
        <input type="hidden" name="contestId" value={contest.id} />

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

        <SaveRow state={state} pending={pending} editable={editable} dict={dict} />
      </Panel>
    </form>
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
  const [state, formAction, pending] = useActionState<SettingsState, FormData>(savePolicyAction, {});
  const [mode, setMode] = useState<string>(policy.mode);

  const toggles = [
    { name: "allowCreateView", on: policy.allowCreateView, label: t.policy.createView },
    { name: "allowOwnTables", on: policy.allowOwnTables, label: t.policy.ownTables },
    { name: "allowTempTables", on: policy.allowTempTables, label: t.policy.tempTables },
    { name: "allowCatalog", on: policy.allowCatalog, label: t.policy.catalog },
  ];

  // Keyed like the contest form; the policy has its own stamp.
  return (
    <form key={policy.updatedAt} action={formAction} className="contents">
      <Panel
        title={t.policy.heading}
        help={t.policy.help}
        dict={dict}
        frozen={editable ? undefined : dict.workspace.facts.frozen}
      >
        <input type="hidden" name="contestId" value={contest.id} />

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

        <SaveRow state={state} pending={pending} editable={editable} dict={dict} />
      </Panel>
    </form>
  );
}
