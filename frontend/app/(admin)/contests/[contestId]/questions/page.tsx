import { Tag } from "@/components/ui/tag";
import { questionListSchema } from "@/lib/api/content";
import { contentEditable } from "@/lib/api/contests";
import { activeDictionary } from "@/lib/i18n/server";

import { loadContest, loadContestResource } from "../contest";
import { QuestionList } from "./question-list";

/**
 * The questions in participant order. A missing reference answer is a
 * publish-gate refusal, so the list marks it while the author writes. The
 * listing omits reference answers and the schema reads an absent list as empty;
 * the mark clears once the question is opened and saved.
 */
export default async function QuestionsPage(props: PageProps<"/contests/[contestId]/questions">) {
  const [{ contestId }, dict] = await Promise.all([props.params, activeDictionary()]);

  const [contest, list] = await Promise.all([
    loadContest(contestId),
    loadContestResource(contestId, "/questions", (payload) => questionListSchema.parse(payload)),
  ]);

  const t = dict.workspace.questions;
  const questions = list?.items ?? [];
  const languages = contest.languages.map((l) => l.code);
  const editable = contentEditable(contest.status);

  return (
    <div className="flex flex-col gap-8">
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-3">
          <h2 className="text-h3 text-ink">{t.heading}</h2>
          {!editable ? <Tag tone="mute">{dict.workspace.facts.frozen}</Tag> : null}
        </div>
        <p className="max-w-body text-body text-ink-2">{t.lede}</p>

        {/* Said up front rather than as a refusal after a second question is written. */}
        {contest.questionMode === "single" ? (
          <p className="max-w-body text-small text-ink-3">{t.single}</p>
        ) : null}

        {!editable ? <p className="max-w-body text-small text-ink-3">{t.frozen}</p> : null}
      </div>

      <QuestionList
        contestId={contest.id}
        questions={questions}
        languages={languages}
        editable={editable}
        scoring={contest.scoring}
        dict={dict}
      />
    </div>
  );
}
