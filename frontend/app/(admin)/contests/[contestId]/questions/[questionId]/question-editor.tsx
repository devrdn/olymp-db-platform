"use client";

import { useActionState, useState } from "react";

import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip } from "@/components/ui/tooltip";
import { MATCH_KINDS, QUESTION_KINDS } from "@/lib/api/content-terms";
import { type Question } from "@/lib/api/content";
import type { Scoring } from "@/lib/api/contests";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { saveQuestionAction, type QuestionState } from "./actions";
import { messageForCode } from "@/lib/i18n/errors";

/**
 * One question in one form with one save, so a partial failure cannot leave
 * part of it committed and a change of kind can move with its options and
 * answers. Sections follow the order of the work: what it is, what it says,
 * what counts as right.
 */
export function QuestionEditor({
  contestId,
  question,
  languages,
  editable,
  sequentialActive,
  scoring,
  dict,
}: {
  contestId: string;
  question: Question;
  languages: string[];
  editable: boolean;
  // Whether sequential progression (docs/ARCHITECTURE.md §6.1.1) is in
  // effect, computed by the page so the rule is not restated here.
  sequentialActive: boolean;
  scoring: Scoring;
  dict: Dictionary;
}) {
  // Option ids are edited in one section and labelled in another, so the label
  // rows must follow what is typed before a save.
  const [choiceIds, setChoiceIds] = useState<string[]>(question.choiceIds);
  const [kind, setKind] = useState(question.kind);

  const [state, formAction, pending] = useActionState<QuestionState, FormData>(
    saveQuestionAction,
    {},
  );

  return (
    <form action={formAction} className="flex flex-col gap-12">
      <input type="hidden" name="contestId" value={contestId} />
      <input type="hidden" name="questionId" value={question.id} />

      <ShapeSection
        question={question}
        editable={editable}
        sequentialActive={sequentialActive}
        scoring={scoring}
        dict={dict}
        kind={kind}
        onKind={setKind}
        choiceIds={choiceIds}
        onChoiceIds={setChoiceIds}
      />

      <TextsSection
        question={question}
        languages={languages}
        choiceIds={kind === "choice" ? choiceIds : []}
        editable={editable}
        dict={dict}
      />

      <AnswersSection
        question={question}
        kind={kind}
        choiceIds={choiceIds}
        editable={editable}
        dict={dict}
      />

      <div className="border-t border-line pt-5">
        <SaveRow state={state} pending={pending} editable={editable} dict={dict} />
      </div>
    </form>
  );
}

/** A titled part of the question, with help behind a "?". */
function Section({
  title,
  help,
  dict,
  children,
}: {
  title: string;
  help: string;
  dict: Dictionary;
  children: React.ReactNode;
}) {
  return (
    <section className="flex flex-col gap-5 border-t border-line pt-5">
      <div className="flex items-center gap-2">
        <h3 className="text-h3 text-ink">{title}</h3>
        <Tooltip label={dict.chrome.helpLabel}>{help}</Tooltip>
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
  state: QuestionState;
  pending: boolean;
  editable: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.question;

  const failure = state.code
    ? (messageForCode(state.code, dict.errors))
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

function ShapeSection({
  question,
  editable,
  sequentialActive,
  scoring,
  dict,
  kind,
  onKind,
  choiceIds,
  onChoiceIds,
}: {
  question: Question;
  editable: boolean;
  sequentialActive: boolean;
  scoring: Scoring;
  dict: Dictionary;
  kind: string;
  onKind: (value: Question["kind"]) => void;
  choiceIds: string[];
  onChoiceIds: (value: string[]) => void;
}) {
  const t = dict.workspace.question;
  // ICPC ignores points and penalty; the fields stay, disabled, since the mode
  // can still be reverted before the start.
  const icpc = scoring === "icpc";

  // Mirrored only for the live preview and warning; the inputs stay
  // uncontrolled so a save shows the server's copy.
  const [points, setPoints] = useState(question.points);
  const [penaltyPct, setPenaltyPct] = useState<number | null>(question.penaltyPct);
  const [unlimitedAttempts, setUnlimitedAttempts] = useState(question.maxAttempts == null);

  const penaltyPerAttempt = Math.floor((points * (penaltyPct ?? 0)) / 100);

  return (
    <>
      <Section title={t.shape.heading} help={t.shape.help} dict={dict}>
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
          <Field
            id="points"
            label={t.shape.points}
            help={t.shape.pointsHelp}
            helpLabel={dict.chrome.helpLabel}
            hint={icpc ? t.shape.icpcDisabled : undefined}
          >
            <Input
              name={icpc ? undefined : "points"}
              type="number"
              min={0}
              step={1}
              defaultValue={question.points}
              onChange={(event) => {
                const value = Number(event.target.value);
                setPoints(Number.isFinite(value) && value >= 0 ? value : 0);
              }}
              disabled={!editable || icpc}
            />
          </Field>
          {/* Disabled inputs are not submitted; without this a wording-only save
             would zero the points under ICPC. */}
          {icpc ? <input type="hidden" name="points" value={points} /> : null}

          <Field id="maxAttempts" label={t.shape.attempts} hint={t.shape.attemptsHint}>
            <Input
              name="maxAttempts"
              type="number"
              min={1}
              step={1}
              defaultValue={question.maxAttempts ?? ""}
              placeholder={t.shape.unlimited}
              onChange={(event) => setUnlimitedAttempts(event.target.value.trim() === "")}
              disabled={!editable}
            />
          </Field>
        </div>

        {/* The publish gate refuses unlimited attempts under sequential
           progression (docs/ARCHITECTURE.md §6.1.1): a stuck participant
           could not move on. */}
        {sequentialActive && unlimitedAttempts ? (
          <p className="max-w-body text-small text-warn">{t.shape.sequentialNeedsAttempts}</p>
        ) : null}
        {/* Likewise for winner scoring: a wrong final answer costs nothing, so
           the gate refuses unlimited attempts. */}
        {scoring === "winner" && kind === "final" && unlimitedAttempts ? (
          <p className="max-w-body text-small text-warn">{t.shape.winnerFinalNeedsAttempts}</p>
        ) : null}

        <Field
          id="penaltyPct"
          label={t.shape.penalty}
          help={t.shape.penaltyHelp}
          helpLabel={dict.chrome.helpLabel}
          hint={icpc ? t.shape.icpcDisabled : undefined}
        >
          <Input
            name={icpc ? undefined : "penaltyPct"}
            type="number"
            min={0}
            max={100}
            step={1}
            defaultValue={question.penaltyPct}
            onChange={(event) => {
              const raw = event.target.value.trim();
              if (raw === "") {
                setPenaltyPct(null);
                return;
              }
              const value = Number(raw);
              setPenaltyPct(Number.isFinite(value) ? value : null);
            }}
            disabled={!editable || icpc}
            className="max-w-40"
          />
        </Field>
        {/* As with `points`: the visible field is disabled under ICPC, so the
           stored penalty is resubmitted here. */}
        {icpc ? (
          <input type="hidden" name="penaltyPct" value={penaltyPct ?? ""} />
        ) : null}

        {/* The penalty worked out for this question (docs/ARCHITECTURE.md
            §6.1.1); not used under ICPC. */}
        {!icpc ? (
          <p className="-mt-3 max-w-body text-small text-ink-3">
            {t.shape.penaltyPreview
              .replace("{n}", String(penaltyPerAttempt))
              .replace("{points}", String(points))}
          </p>
        ) : null}

        {/* The API refuses options on any other kind. */}
        {kind === "choice" ? (
          <Field
            id="choiceIds"
            label={t.shape.choices}
            hint={t.shape.choicesHint}
            help={t.shape.choicesHelp}
            helpLabel={dict.chrome.helpLabel}
          >
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

        {/* Beside the label, so its name does not join the checkbox's. */}
        <div className="flex items-center gap-2">
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
          <Tooltip label={dict.chrome.helpLabel}>{t.shape.visibleHelp}</Tooltip>
        </div>

      </Section>
    </>
  );
}

function TextsSection({
  question,
  languages,
  choiceIds,
  editable,
  dict,
}: {
  question: Question;
  languages: string[];
  choiceIds: string[];
  editable: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.question;
  return (
    <>
      <Section title={t.texts.heading} help={t.texts.help} dict={dict}>
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

                {/* One label per option per language; the id stays fixed, which
                   keeps checking language-independent. */}
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

      </Section>
    </>
  );
}

function AnswersSection({
  question,
  kind,
  choiceIds,
  editable,
  dict,
}: {
  question: Question;
  kind: string;
  choiceIds: string[];
  editable: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.question;
  // One spare row, so adding an answer needs no button or client state.
  const rows = [...question.answers, { id: undefined, matchKind: "exact" as const, value: "" }];
  const [extra, setExtra] = useState(0);

  return (
    <>
      <Section title={t.answers.heading} help={t.answers.help} dict={dict}>
        <div className="flex flex-col gap-3">
          {[...rows, ...Array.from({ length: extra }, () => null)].map((answer, index) => (
            <div key={index} className="flex flex-wrap items-center gap-3">
              {/* A choice answer must be one of the option ids, so the field offers them. */}
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

        {/* The action drops blank rows, so clearing a field removes the answer. */}
        <p className="max-w-body text-small text-ink-3">{t.answers.removeHint}</p>
        {/* Patterns are anchored to the whole answer (backend
           `compileAnswerPattern`), unlike substring matching. */}
        <p className="max-w-body text-small text-ink-3">{t.answers.regexHint}</p>

      </Section>
    </>
  );
}
