import Link from "next/link";
import { notFound, unstable_rethrow } from "next/navigation";

import { Tag } from "@/components/ui/tag";
import { questionListSchema, questionSchema } from "@/lib/api/content";
import { contentEditable, sequentialActive } from "@/lib/api/contests";
import { isId } from "@/lib/api/ids";
import { activeDictionary } from "@/lib/i18n/server";

import { loadContest, loadContestResource } from "../../contest";
import { QuestionEditor } from "./question-editor";

/**
 * The contest's questions, for an advisory hint only: a failed read must not
 * take the editor down, so it answers undefined. Redirects and not-found
 * signals still propagate.
 */
async function otherQuestions(contestId: string) {
  try {
    const list = await loadContestResource(contestId, "/questions", (payload) =>
      questionListSchema.parse(payload),
    );
    return list?.items;
  } catch (error) {
    unstable_rethrow(error);
    return undefined;
  }
}

/**
 * One question. The API checks the question belongs to this contest (its
 * middleware checks only the contest in the URL) and answers 404, not 403, so
 * the question's existence is not revealed.
 */
export default async function QuestionPage(
  props: PageProps<"/contests/[contestId]/questions/[questionId]">,
) {
  const [{ contestId, questionId }, dict] = await Promise.all([props.params, activeDictionary()]);
  if (!isId(questionId)) notFound();

  const [contest, question, list] = await Promise.all([
    loadContest(contestId),
    loadContestResource(contestId, `/questions/${questionId}`, (payload) =>
      questionSchema.parse(payload),
    ),
    otherQuestions(contestId),
  ]);

  if (!question) notFound();

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
          <h2 className="text-h3 text-ink">{t.heading.replace("{n}", String(question.ord))}</h2>
          {!question.isVisible ? (
            <Tag tone="mute">{dict.workspace.questions.hidden}</Tag>
          ) : null}
          {question.isBonus ? <Tag tone="good">{dict.workspace.questions.bonus}</Tag> : null}
          {!editable ? <Tag tone="mute">{dict.workspace.facts.frozen}</Tag> : null}
        </div>
      </div>

      <QuestionEditor
        contestId={contest.id}
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
