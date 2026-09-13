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

/**
 * One question, in one form with one save.
 *
 * It was three forms saving separately, on the reasoning that they were three
 * endpoints. That had the argument backwards: the endpoints were three because
 * nothing had put them together, and an author never edits a third of a
 * question — they edit the question.
 *
 * Three saves also made two things impossible. A failure in the second left
 * the first already committed, under a button that had said "saved". And a
 * change of kind could not be expressed at all: turning a typed question into
 * a choice question needs the kind, the options and the answers to move
 * together, and sent separately each half was refused on account of the other.
 *
 * The order is still the order the work happens in: what the question *is*,
 * then what it says, then what counts as right.
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
  // Whether this contest's own sequential progression (§6.1.1) is actually
  // in effect — contests.sequentialActive on the wire side, computed once by
  // the page from the contest it already loaded, rather than this editor
  // reading contest.progression and contest.questionMode itself and risking
  // a second copy of that rule (finding 4).
  sequentialActive: boolean;
  // The contest's own scoring mode, read once by the page from the contest
  // it already loaded. ICPC scoring (decision 1 of the design doc) does not
  // use a question's own points or percentage penalty at all — the fields
  // stay in the data, since the mode can still be reverted before the
  // contest starts, but the editor disables them here.
  scoring: Scoring;
  dict: Dictionary;
}) {
  // The option identifiers are edited in the shape form and labelled in the
  // texts form, so the label rows have to follow what is typed above them
  // before either is saved.
  const [choiceIds, setChoiceIds] = useState<string[]>(question.choiceIds);
  const [kind, setKind] = useState(question.kind);

  const [state, formAction, pending] = useActionState<QuestionState, FormData>(
    saveQuestionAction,
    {},
  );

  return (
    // One form and one save. The question is one thing an author edits, and it
    // used to be saved in three requests — three chances for the second to
    // fail after the first had landed. It is also the only shape in which a
    // change of kind and its answers can be expressed at all: sent separately,
    // each half was refused on account of the other.
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

      {/* One save row for the whole question, at the end of everything it
          saves — not three, each claiming a third of the same object. */}
      <div className="border-t border-line pt-5">
        <SaveRow state={state} pending={pending} editable={editable} dict={dict} />
      </div>
    </form>
  );
}

/** A titled part of the question; what it covers sits behind a "?" beside the heading. */
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
  // ICPC scoring does not use a question's own points or percentage penalty
  // (decision 1 of the design doc) — the fields stay in the form, disabled,
  // with whatever they already held, rather than disappearing: the mode can
  // still be reverted to `points` or `winner` before the contest starts.
  const icpc = scoring === "icpc";

  // Tracked only so the penalty preview and the sequential-attempts warning
  // below can react as an organizer types, the same reason choiceIds above is
  // mirrored into state — the inputs themselves stay uncontrolled
  // (defaultValue), so a save that revalidates the page still shows the
  // server's own copy rather than fighting it.
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
          {/* Disabled inputs are excluded from FormData entirely, so without
              this a save that only touched the wording would submit
              `points: 0` and silently zero out the question's own points the
              moment its contest turned ICPC. */}
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

        {/* The publish gate refuses exactly this combination under sequential
            progression (§6.1.1): a stuck participant would have nothing left
            to move on to. Said here, at the setting that would trigger it,
            rather than left for an organizer to discover at publish time. */}
        {sequentialActive && unlimitedAttempts ? (
          <p className="max-w-body text-small text-warn">{t.shape.sequentialNeedsAttempts}</p>
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
        {/* Same reasoning as the hidden `points` field above: a pointer on
            the wire, and blank already means "leave the stored penalty
            alone" (QuestionBody's own doc) — but ICPC always disables the
            visible field, so without this the stored penalty would never be
            resubmitted at all, only ever left as it was the day scoring
            changed. */}
        {icpc ? (
          <input type="hidden" name="penaltyPct" value={penaltyPct ?? ""} />
        ) : null}

        {/* What the setting above actually means for this question, worked
            out instead of left for an organizer to compute by hand (§6.1.1).
            Meaningless in ICPC scoring, where the penalty is not used at all. */}
        {!icpc ? (
          <p className="-mt-3 max-w-body text-small text-ink-3">
            {t.shape.penaltyPreview
              .replace("{n}", String(penaltyPerAttempt))
              .replace("{points}", String(points))}
          </p>
        ) : null}

        {/* Only a choice question has options, and the API refuses them on any
            other kind — so the field disappears with the kind rather than
            sending values that would be rejected. */}
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

        {/* The "?" beside the label, not inside it: inside, its name would
            become part of the checkbox's own. */}
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
  // One spare row, so adding an answer needs no button and no client state.
  const rows = [...question.answers, { id: undefined, matchKind: "exact" as const, value: "" }];
  const [extra, setExtra] = useState(0);

  return (
    <>
      <Section title={t.answers.heading} help={t.answers.help} dict={dict}>
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

      </Section>
    </>
  );
}
