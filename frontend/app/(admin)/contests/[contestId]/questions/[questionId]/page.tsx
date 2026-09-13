import Link from "next/link";
import { notFound } from "next/navigation";

import { Tag } from "@/components/ui/tag";
import { questionSchema } from "@/lib/api/content";
import { contentEditable, sequentialActive } from "@/lib/api/contests";
import { isId } from "@/lib/api/ids";
import { activeDictionary } from "@/lib/i18n/server";

import { loadContest, loadContestResource } from "../../contest";
import { QuestionEditor } from "./question-editor";

/**
 * One question.
 *
 * The API checks that the question belongs to this contest and answers 404
 * rather than 403 when it does not: without that check the owner of one
 * contest could reach another's question by guessing an identifier, and the
 * middleware would wave it through, because it checked the contest in the URL.
 * Saying "forbidden" would be its own leak — that somebody else's question
 * exists is not this account's business.
 */
export default async function QuestionPage(
  props: PageProps<"/contests/[contestId]/questions/[questionId]">,
) {
  const [{ contestId, questionId }, dict] = await Promise.all([props.params, activeDictionary()]);
  if (!isId(questionId)) notFound();

  const [contest, question] = await Promise.all([
    loadContest(contestId),
    loadContestResource(contestId, `/questions/${questionId}`, (payload) =>
      questionSchema.parse(payload),
    ),
  ]);

  if (!question) notFound();

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
          {/* `ord` is the contest's own numbering and starts at one, which is
              the number the question list prints beside this question. Adding
              to it here would have the two screens disagree about which
              question is open. */}
          <h2 className="text-h3 text-ink">{t.heading.replace("{n}", String(question.ord))}</h2>
          {!question.isVisible ? (
            <Tag tone="mute">{dict.workspace.questions.hidden}</Tag>
          ) : null}
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
        dict={dict}
      />
    </div>
  );
}
