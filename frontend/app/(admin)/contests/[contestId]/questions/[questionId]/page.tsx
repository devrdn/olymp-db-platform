import Link from "next/link";
import { notFound } from "next/navigation";

import { Tag } from "@/components/ui/tag";
import { questionListSchema, questionSchema } from "@/lib/api/content";
import { sequentialActive } from "@/lib/api/contests";
import { contentEditable } from "@/lib/api/contests-terms";
import { isId } from "@/lib/api/ids";
import { activeDictionary } from "@/lib/i18n/server";
import { fill } from "@/lib/i18n/fill";

import { loadContest, loadContestResource } from "../../contest";
import { QuestionEditor } from "./question-editor";

/** The contest's questions, for an advisory hint only: a failed read must not take the editor down. */
async function otherQuestions(contestId: string) {
  const list = await loadContestResource(contestId, "/questions", (payload) => questionListSchema.parse(payload), {
    bestEffort: true,
  });
  return list?.items;
}

/**
 * One question. The API checks the question belongs to this contest (its
 * middleware checks only the contest in the URL) and answers
 * `question_not_found`, not `forbidden`, so the question's existence is not
 * revealed.
 */
export default async function QuestionPage(
  props: PageProps<"/contests/[contestId]/questions/[questionId]">,
) {
  const [{ contestId, questionId }, dict] = await Promise.all([props.params, activeDictionary()]);
  if (!isId(questionId)) notFound();

  const [contest, question, list] = await Promise.all([
    loadContest(contestId),
    loadContestResource(contestId, `/questions/${questionId}`, (payload) => questionSchema.parse(payload), {
      missing: ["question_not_found"],
    }),
    otherQuestions(contestId),
  ]);

  const otherVisible = list?.filter((q) => q.id !== question.id && q.isVisible);
  const otherVisibleFinals = otherVisible?.filter((q) => q.kind === "final").length;

  const t = dict.workspace.question;
  const editable = contentEditable(contest.status);

  return (
    <div className="flex flex-col gap-8">
      <div className="flex flex-col gap-3">
        <Link
          href={`/contests/${contest.id}/questions`}
          className="w-fit font-mono text-data text-ink-3 transition-colors duration-(--t-input) ease-standard hover:text-ink"
        >
          {t.back}
        </Link>

        <div className="flex flex-wrap items-center gap-3">
          {/* `ord` starts at one and matches the number in the question list. */}
          <h2 className="text-h3 text-ink">{fill(t.heading, { n: question.ord })}</h2>
          {!question.isVisible ? (
            <Tag tone="mute">{dict.workspace.questions.hidden}</Tag>
          ) : null}
          {question.isBonus ? <Tag tone="good">{dict.workspace.questions.bonus}</Tag> : null}
          {!editable ? <Tag tone="mute">{dict.workspace.facts.frozen}</Tag> : null}
        </div>
      </div>

      <QuestionEditor
        contest={contest}
        question={question}
        languages={contest.languages.map((l) => l.code)}
        editable={editable}
        sequentialActive={sequentialActive(contest)}
        scoring={contest.scoring}
        otherVisibleQuestions={otherVisible?.length}
        otherVisibleFinals={otherVisibleFinals}
        dict={dict}
      />
    </div>
  );
}
