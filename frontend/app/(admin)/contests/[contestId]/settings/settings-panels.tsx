"use client";

import { useActionState, useState } from "react";

import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Tag } from "@/components/ui/tag";
import {
  ENROLLMENTS,
  QUESTION_MODES,
  TIMINGS,
  type Contest,
  type ContestSummary,
} from "@/lib/api/contests";
import { SQL_MODES, type SqlPolicy } from "@/lib/api/policy";
import { wallClockFromInstant } from "@/lib/format/datetime";
import { LOCALES, LOCALE_NAMES, type Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import {
  saveLanguagesAction,
  savePolicyAction,
  saveSettingsAction,
  saveTranslationsAction,
  type SettingsState,
} from "./actions";

/**
 * A titled block with its own save.
 *
 * Four forms, not one, because they are four endpoints: the contest's own
 * fields, the language set, the translations and the SQL policy. A single save
 * would send all four on every change, and a refusal from one would discard
 * the other three.
 */
function Panel({
  title,
  hint,
  frozen,
  children,
}: {
  title: string;
  hint?: string;
  frozen?: string;
  children: React.ReactNode;
}) {
  return (
    <section className="flex flex-col gap-5 border-t border-line pt-5">
      <div className="flex flex-col gap-1.5">
        <div className="flex flex-wrap items-center gap-3">
          <h3 className="text-h3 text-ink">{title}</h3>
          {frozen ? <Tag tone="mute">{frozen}</Tag> : null}
        </div>
        {hint ? <p className="max-w-body text-small text-ink-2">{hint}</p> : null}
      </div>
      {children}
    </section>
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
    ? ((dict.errors as Record<string, string>)[state.code] ?? dict.errors.fallback)
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

      {/* The server answers one code for a bad policy; the entries that were
          actually wrong are only knowable on this side, so they are named. */}
      {state.rejected && state.rejected.length > 0 ? (
        <p className="max-w-body font-mono text-data text-warn">{state.rejected.join(", ")}</p>
      ) : null}
    </div>
  );
}

/** One choice from a short, closed set — a radio group, not a select. */
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
 * The contest's own fields.
 *
 * Two freezes apply and both are shown. Settings stay editable while the
 * contest runs — extending the window after a power cut is exactly what a
 * running contest needs — but the shape does not, because people are already
 * answering under it. The frozen controls stay in the form and stay disabled:
 * removing them would make the form's own values incomplete, and `PATCH` here
 * writes every field it is given.
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

  return (
    <form action={formAction} className="contents">
      <Panel title={t.schedule.heading} hint={t.schedule.hint}>
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

          <Field id="endsAt" label={t.schedule.endsAt}>
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

          <Field id="gracePeriodMin" label={t.schedule.grace} hint={t.schedule.graceHint}>
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
        hint={t.shape.hint}
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
      </Panel>

      <Panel title={t.access.heading} hint={t.access.hint}>
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

/**
 * The language set and the titles.
 *
 * Two forms, because they are two endpoints and the order matters: a language
 * has to be declared before there is anywhere to put its title. They sit
 * together because that is the order an author works in.
 */
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
      <Panel title={t.languages.heading} hint={t.languages.hint}>
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

        {/* Dropping a language drops its texts with it. Said before the save,
            not discovered after it. */}
        <p className="max-w-body text-small text-ink-3">{t.languages.warning}</p>

        <SaveRow state={state} pending={pending} editable={editable} dict={dict} />
      </Panel>
    </form>
  );
}

export function TranslationPanel({
  contest,
  editable,
  dict,
}: {
  contest: Contest;
  editable: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.settings;
  const [state, formAction, pending] = useActionState<SettingsState, FormData>(
    saveTranslationsAction,
    {},
  );

  const declared = contest.languages.map((l) => l.code);

  return (
    <form action={formAction} className="contents">
      <Panel title={t.titles.heading} hint={t.titles.hint}>
        <input type="hidden" name="contestId" value={contest.id} />

        {declared.length === 0 ? (
          <p className="max-w-body text-body text-ink-3">{dict.workspace.story.noLanguages}</p>
        ) : (
          <div className="grid gap-8 narrow:grid-cols-2">
            {declared.map((lang) => (
              <div key={lang} className="flex flex-col gap-4">
                <span className="font-mono text-label text-ink uppercase">{lang}</span>

                <Field id={`title-${lang}`} label={t.titles.title}>
                  <Input
                    name={`title.${lang}`}
                    defaultValue={contest.translations[lang]?.title ?? ""}
                    disabled={!editable}
                  />
                </Field>

                <Field id={`description-${lang}`} label={t.titles.description}>
                  <Input
                    name={`description.${lang}`}
                    defaultValue={contest.translations[lang]?.description ?? ""}
                    disabled={!editable}
                  />
                </Field>
              </div>
            ))}
          </div>
        )}

        <SaveRow state={state} pending={pending} editable={editable} dict={dict} />
      </Panel>
    </form>
  );
}

/**
 * What participants may do to their copy of the game database.
 *
 * It freezes with the content, not with the settings: a policy that changed
 * mid-contest would give participants different rights depending on when they
 * connected, and the template's grants would drift from what the validator
 * enforces.
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

  return (
    <form action={formAction} className="contents">
      <Panel
        title={t.policy.heading}
        hint={t.policy.hint}
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

        {/* Only a read-write policy has writable tables. Offered under a
            read-only mode, the field would describe access that mode does not
            grant. */}
        {mode === "read_write" ? (
          <Field id="writableTables" label={t.policy.tables} hint={t.policy.tablesHint}>
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
