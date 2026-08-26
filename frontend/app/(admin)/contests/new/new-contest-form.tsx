"use client";

import { useActionState, useState } from "react";

import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { ENROLLMENTS, QUESTION_MODES, TIMINGS } from "@/lib/api/contests";
import { LOCALES, LOCALE_NAMES, type Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { createContestAction, type NewContestState } from "./actions";

const FAILURE_ID = "new-contest-failure";

/**
 * Everything that has to be decided before a contest exists.
 *
 * Which is less than it looks, and deliberately so. The schedule, the network
 * restriction and the rate limits are not here: a draft has none of them, they
 * stay editable while the contest runs, and asking for them now would be
 * asking an author to invent a date to get past a form.
 *
 * What *is* here freezes. The question format and the timing model stop being
 * editable when the contest starts, because people are already answering under
 * them; and the language set decides what every editor after this one asks
 * for. Those are the questions worth a screen of their own.
 *
 * The language set is the only part with client state, and it earns it: the
 * title fields are per language, so the form has to grow and shrink as
 * languages are ticked. Without JavaScript every language's title field is
 * present and the checkboxes still submit, so the form degrades to a longer
 * version of itself rather than to a broken one.
 */
export function NewContestForm({ dict, locale }: { dict: Dictionary; locale: Locale }) {
  const t = dict.workspace.create;
  const [state, formAction, pending] = useActionState<NewContestState, FormData>(
    createContestAction,
    {},
  );

  const [chosen, setChosen] = useState<Locale[]>([locale]);
  const [fallback, setFallback] = useState<Locale>(locale);
  const [timing, setTiming] = useState<string>("fixed");

  const failure = state.code
    ? ((dict.errors as Record<string, string>)[state.code] ?? dict.errors.fallback)
    : null;

  function toggle(code: Locale, on: boolean) {
    setChosen((current) => {
      const next = on ? [...current, code] : current.filter((c) => c !== code);
      // The contest must fall back to a language it actually declares.
      if (!next.includes(fallback) && next[0]) setFallback(next[0]);
      return next;
    });
  }

  return (
    <form action={formAction} className="flex max-w-2xl flex-col gap-10" noValidate>
      <Group legend={t.languages.legend} hint={t.languages.hint}>
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
                  onChange={(event) => toggle(code, event.target.checked)}
                  className="size-4 cursor-pointer accent-cta"
                />
                <label htmlFor={`lang-${code}`} className="cursor-pointer text-control text-ink">
                  {LOCALE_NAMES[code]}
                  <span className="ml-2 font-mono text-data text-ink-3 uppercase">{code}</span>
                </label>

                {/* The default is a property of the set, so the control for it
                    lives with the set rather than in a select of its own. */}
                <label
                  className={cn(
                    "ml-auto flex items-center gap-2 text-small",
                    on ? "cursor-pointer text-ink-2" : "text-ink-3 opacity-45",
                  )}
                >
                  <input
                    type="radio"
                    name="defaultLanguage"
                    value={code}
                    checked={fallback === code}
                    disabled={!on}
                    onChange={() => setFallback(code)}
                    className="size-3.5 cursor-pointer accent-cta disabled:cursor-not-allowed"
                  />
                  {t.languages.fallback}
                </label>
              </div>
            );
          })}
        </div>
      </Group>

      <Group legend={t.titles.legend} hint={t.titles.hint}>
        <div className="flex flex-col gap-6">
          {chosen.map((code) => (
            <div key={code} className="flex flex-col gap-4 border-l-2 border-line-2 pl-4">
              <span className="font-mono text-label text-ink-3 uppercase">
                {LOCALE_NAMES[code]}
                {code === fallback ? ` · ${t.languages.fallback}` : ""}
              </span>

              <Field
                id={`title-${code}`}
                label={t.titles.title}
                invalid={Boolean(failure) && code === fallback}
                describedBy={failure ? FAILURE_ID : undefined}
              >
                <Input name={`title.${code}`} required={code === fallback} />
              </Field>

              <Field id={`description-${code}`} label={t.titles.description} hint={t.titles.optional}>
                <Input name={`description.${code}`} />
              </Field>
            </div>
          ))}
        </div>
      </Group>

      <Group legend={t.format.legend} hint={t.format.hint}>
        <Choices
          name="questionMode"
          values={QUESTION_MODES}
          labels={dict.contests.mode}
          initial="multi"
        />
      </Group>

      <Group legend={t.timingGroup.legend} hint={t.timingGroup.hint}>
        <div className="flex flex-col gap-5">
          <Choices
            name="timing"
            values={TIMINGS}
            labels={dict.workspace.timing}
            initial="fixed"
            onPick={setTiming}
          />

          {/* Only asked for when it means something. On the shared window the
              deadline is the contest's end, and a duration field there would
              be a value with nothing to apply to. */}
          {timing === "individual" ? (
            <Field id="durationMin" label={t.timingGroup.duration} hint={t.timingGroup.durationHint}>
              <Input
                name="durationMin"
                type="number"
                min={1}
                step={1}
                defaultValue={90}
                required
                className="max-w-40"
              />
            </Field>
          ) : null}
        </div>
      </Group>

      <Group legend={t.enrollmentGroup.legend} hint={t.enrollmentGroup.hint}>
        <Choices
          name="enrollment"
          values={ENROLLMENTS}
          labels={dict.contests.enrollment}
          initial="invite_only"
        />
      </Group>

      <div className="flex flex-col gap-3">
        {failure ? (
          <p id={FAILURE_ID} role="alert" className="max-w-body text-small text-bad">
            {failure}
          </p>
        ) : null}

        <Button type="submit" disabled={pending} className="self-start">
          {pending ? t.submitting : t.submit}
        </Button>
      </div>
    </form>
  );
}

/**
 * A titled block of the form.
 *
 * A real `<fieldset>` and `<legend>`: a group of radios announced without one
 * is a list of options with no question attached, which is exactly how a
 * screen reader meets "fixed / individual".
 */
function Group({
  legend,
  hint,
  children,
}: {
  legend: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <fieldset className="flex flex-col gap-4 border-t border-line pt-5">
      <div className="flex flex-col gap-1.5">
        <legend className="text-h3 text-ink">{legend}</legend>
        {hint ? <p className="max-w-body text-small text-ink-2">{hint}</p> : null}
      </div>
      {children}
    </fieldset>
  );
}

/** One choice from a short, closed set — a radio group, not a select. */
function Choices<T extends string>({
  name,
  values,
  labels,
  initial,
  onPick,
}: {
  name: string;
  values: readonly T[];
  labels: Record<string, string>;
  initial: T;
  onPick?: (value: T) => void;
}) {
  const [picked, setPicked] = useState<T>(initial);

  return (
    <div className="flex flex-col gap-2.5">
      {values.map((value) => (
        <label key={value} className="flex cursor-pointer items-center gap-3 text-control text-ink">
          <input
            type="radio"
            name={name}
            value={value}
            checked={picked === value}
            onChange={() => {
              setPicked(value);
              onPick?.(value);
            }}
            className="size-4 cursor-pointer accent-cta"
          />
          {labels[value]}
        </label>
      ))}
    </div>
  );
}
