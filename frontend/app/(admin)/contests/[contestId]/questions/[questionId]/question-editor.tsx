"use client";

import { useActionState, useState } from "react";

import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { MATCH_KINDS, QUESTION_KINDS, type Question } from "@/lib/api/content";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import {
  saveAnswersAction,
  saveQuestionAction,
  saveTextsAction,
  type QuestionState,
} from "./actions";

/**
 * One question, in three forms that save separately.
 *
 * Separately because they are three endpoints with three different meanings,
 * and because an author fixing a typo in the Romanian body should not have to
 * re-submit the reference answers to do it. A single "save everything" button
 * would also make a failure in one part discard the other two.
 *
 * The order is the order the work happens in: what the question *is*, then
 * what it says, then what counts as right.
 */
export function QuestionEditor({
  contestId,
  question,
  languages,
  editable,
  dict,
}: {
  contestId: string;
  question: Question;
  languages: string[];
  editable: boolean;
  dict: Dictionary;
}) {
  // The option identifiers are edited in the shape form and labelled in the
  // texts form, so the label rows have to follow what is typed above them
  // before either is saved.
  const [choiceIds, setChoiceIds] = useState<string[]>(question.choiceIds);
  const [kind, setKind] = useState(question.kind);

  return (
    <div className="flex flex-col gap-12">
      <ShapeForm
        contestId={contestId}
        question={question}
        editable={editable}
        dict={dict}
        kind={kind}
        onKind={setKind}
        choiceIds={choiceIds}
        onChoiceIds={setChoiceIds}
      />

      <TextsForm
        contestId={contestId}
        question={question}
        languages={languages}
        choiceIds={kind === "choice" ? choiceIds : []}
        editable={editable}
        dict={dict}
      />

      <AnswersForm
        contestId={contestId}
        question={question}
        kind={kind}
        choiceIds={choiceIds}
        editable={editable}
        dict={dict}
      />
    </div>
  );
}

function Section({
  title,
  hint,
  children,
}: {
  title: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <section className="flex flex-col gap-5 border-t border-line pt-5">
      <div className="flex flex-col gap-1.5">
        <h3 className="text-h3 text-ink">{title}</h3>
        {hint ? <p className="max-w-body text-small text-ink-2">{hint}</p> : null}
      </div>
      {children}
    </section>
  );
}

/** The save control and whatever the last attempt had to say about itself. */
function SaveRow({
  state,
  pending,
  editable,
  dict,
}: {
  state: QuestionState;
  pending: boolean;
  editable: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.question;

  const failure = state.code
    ? ((dict.errors as Record<string, string>)[state.code] ?? dict.errors.fallback)
    : null;

  return (
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
  );
}

function ShapeForm({
  contestId,
  question,
  editable,
  dict,
  kind,
  onKind,
  choiceIds,
  onChoiceIds,
}: {
  contestId: string;
  question: Question;
  editable: boolean;
  dict: Dictionary;
  kind: string;
  onKind: (value: Question["kind"]) => void;
  choiceIds: string[];
  onChoiceIds: (value: string[]) => void;
}) {
  const t = dict.workspace.question;
  const [state, formAction, pending] = useActionState<QuestionState, FormData>(
    saveQuestionAction,
    {},
  );

  return (
    <form action={formAction} className="contents">
      <Section title={t.shape.heading} hint={t.shape.hint}>
        <input type="hidden" name="contestId" value={contestId} />
        <input type="hidden" name="questionId" value={question.id} />

        <fieldset className="flex flex-col gap-2.5">
          <legend className="pb-2 font-mono text-label text-ink-3 uppercase">{t.shape.kind}</legend>
          {QUESTION_KINDS.map((value) => (
            <label
              key={value}
              className="flex cursor-pointer items-baseline gap-3 text-control text-ink"
            >
              <input
                type="radio"
                name="kind"
                value={value}
                checked={kind === value}
                disabled={!editable}
                onChange={() => onKind(value)}
                className="size-4 cursor-pointer accent-cta disabled:cursor-not-allowed"
              />
              <span>
                {dict.workspace.questions.kind[value]}
                <span className="ml-2 text-small text-ink-3">{t.shape.kindHint[value]}</span>
              </span>
            </label>
          ))}
        </fieldset>

        <div className="grid gap-6 narrow:grid-cols-2">
          <Field id="points" label={t.shape.points} hint={t.shape.pointsHint}>
            <Input
              name="points"
              type="number"
              min={0}
              step={1}
              defaultValue={question.points}
              disabled={!editable}
            />
          </Field>

          <Field id="maxAttempts" label={t.shape.attempts} hint={t.shape.attemptsHint}>
            <Input
              name="maxAttempts"
              type="number"
              min={1}
              step={1}
              defaultValue={question.maxAttempts ?? ""}
              placeholder={t.shape.unlimited}
              disabled={!editable}
            />
          </Field>
        </div>

        {/* Only a choice question has options, and the API refuses them on any
            other kind — so the field disappears with the kind rather than
            sending values that would be rejected. */}
        {kind === "choice" ? (
          <Field id="choiceIds" label={t.shape.choices} hint={t.shape.choicesHint}>
            <Input
              name="choiceIds"
              defaultValue={choiceIds.join(", ")}
              onChange={(event) =>
                onChoiceIds(
                  event.target.value
                    .split(/[\s,;]+/)
                    .map((id) => id.trim())
                    .filter(Boolean),
                )
              }
              disabled={!editable}
            />
          </Field>
        ) : null}

        <label className="flex w-fit cursor-pointer items-center gap-3 text-control text-ink">
          <input
            type="checkbox"
            name="isVisible"
            defaultChecked={question.isVisible}
            disabled={!editable}
            className="size-4 cursor-pointer accent-cta disabled:cursor-not-allowed"
          />
          {t.shape.visible}
        </label>
        <p className="-mt-3 max-w-body text-small text-ink-3">{t.shape.visibleHint}</p>

        <SaveRow state={state} pending={pending} editable={editable} dict={dict} />
      </Section>
    </form>
  );
}

function TextsForm({
  contestId,
  question,
  languages,
  choiceIds,
  editable,
  dict,
}: {
  contestId: string;
  question: Question;
  languages: string[];
  choiceIds: string[];
  editable: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.question;
  const [state, formAction, pending] = useActionState<QuestionState, FormData>(saveTextsAction, {});

  return (
    <form action={formAction} className="contents">
      <Section title={t.texts.heading} hint={t.texts.hint}>
        <input type="hidden" name="contestId" value={contestId} />
        <input type="hidden" name="questionId" value={question.id} />

        {languages.length === 0 ? (
          <p className="max-w-body text-body text-ink-3">{dict.workspace.story.noLanguages}</p>
        ) : (
          <div className="grid gap-8 narrow:grid-cols-2">
            {languages.map((lang) => (
              <div key={lang} className="flex flex-col gap-3">
                <label
                  htmlFor={`body-${lang}`}
                  className="font-mono text-label text-ink uppercase"
                >
                  {lang}
                </label>

                <Textarea
                  id={`body-${lang}`}
                  name={`body.${lang}`}
                  defaultValue={question.texts[lang]?.bodyMd ?? ""}
                  disabled={!editable}
                  className="min-h-28"
                  placeholder={t.texts.placeholder}
                />

                {/* One label per option, per language. The identifier stays put
                    and only the wording changes, which is what makes checking a
                    choice question language-independent. */}
                {choiceIds.map((choiceId) => (
                  <div key={choiceId} className="flex items-center gap-3">
                    <span className="w-10 shrink-0 font-mono text-data text-ink-3">{choiceId}</span>
                    <Input
                      name={`choice.${lang}.${choiceId}`}
                      defaultValue={question.texts[lang]?.choices?.[choiceId] ?? ""}
                      disabled={!editable}
                      aria-label={`${t.texts.choiceLabel} ${choiceId} (${lang})`}
                    />
                  </div>
                ))}
              </div>
            ))}
          </div>
        )}

        <SaveRow state={state} pending={pending} editable={editable} dict={dict} />
      </Section>
    </form>
  );
}

function AnswersForm({
  contestId,
  question,
  kind,
  choiceIds,
  editable,
  dict,
}: {
  contestId: string;
  question: Question;
  kind: string;
  choiceIds: string[];
  editable: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.question;
  const [state, formAction, pending] = useActionState<QuestionState, FormData>(
    saveAnswersAction,
    {},
  );

  // One spare row, so adding an answer needs no button and no client state.
  const rows = [...question.answers, { id: undefined, matchKind: "exact" as const, value: "" }];
  const [extra, setExtra] = useState(0);

  return (
    <form action={formAction} className="contents">
      <Section title={t.answers.heading} hint={t.answers.hint}>
        <input type="hidden" name="contestId" value={contestId} />
        <input type="hidden" name="questionId" value={question.id} />

        <div className="flex flex-col gap-3">
          {[...rows, ...Array.from({ length: extra }, () => null)].map((answer, index) => (
            <div key={index} className="flex flex-wrap items-center gap-3">
              {/* A choice question's answer must be one of its own option
                  identifiers, so the field offers them instead of free text —
                  an answer nobody could submit is one the author only finds out
                  about when the results are worked out. */}
              {kind === "choice" && choiceIds.length > 0 ? (
                <select
                  name="answerValue"
                  defaultValue={answer?.value ?? ""}
                  disabled={!editable}
                  className={cn(
                    "h-(--control-h) min-w-40 rounded-none border border-edge bg-transparent px-3",
                    "text-control text-ink transition-colors duration-(--t-input) ease-standard",
                    "hover:border-ink-2 focus-visible:border-ink",
                    "disabled:cursor-not-allowed disabled:bg-sunk disabled:text-ink-3",
                  )}
                  aria-label={t.answers.value}
                >
                  <option value="">{t.answers.pick}</option>
                  {choiceIds.map((choiceId) => (
                    <option key={choiceId} value={choiceId}>
                      {choiceId}
                    </option>
                  ))}
                </select>
              ) : (
                <Input
                  name="answerValue"
                  defaultValue={answer?.value ?? ""}
                  disabled={!editable}
                  placeholder={t.answers.value}
                  aria-label={t.answers.value}
                  className="max-w-80"
                />
              )}

              <select
                name="answerKind"
                defaultValue={answer?.matchKind ?? "exact"}
                disabled={!editable}
                className={cn(
                  "h-(--control-h) rounded-none border border-edge bg-transparent px-3",
                  "text-control text-ink transition-colors duration-(--t-input) ease-standard",
                  "hover:border-ink-2 focus-visible:border-ink",
                  "disabled:cursor-not-allowed disabled:bg-sunk disabled:text-ink-3",
                )}
                aria-label={t.answers.matchKind}
              >
                {MATCH_KINDS.map((value) => (
                  <option key={value} value={value}>
                    {t.answers.match[value]}
                  </option>
                ))}
              </select>
            </div>
          ))}
        </div>

        {editable ? (
          <Button
            type="button"
            variant="quiet"
            size="sm"
            className="self-start"
            onClick={() => setExtra((n) => n + 1)}
          >
            {t.answers.addRow}
          </Button>
        ) : null}

        {/* An empty row is how an answer is removed: the action drops blanks,
            so clearing a field and saving is the deletion. Said out loud,
            because a control that is absent has to be explained. */}
        <p className="max-w-body text-small text-ink-3">{t.answers.removeHint}</p>

        <SaveRow state={state} pending={pending} editable={editable} dict={dict} />
      </Section>
    </form>
  );
}
