import { StoryText } from "@/components/product/story-text";
import { Tag } from "@/components/ui/tag";
import type { ArchiveContest } from "@/lib/api/archive";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { plural } from "@/lib/i18n/plural";

/**
 * The tasks of a past contest, read-only: the story, the table notes and the
 * questions as participants saw them. Every text is the organiser's Markdown,
 * so each goes through `StoryText`, which never produces an HTML string. A
 * choice question lists its labels as plain text; there is nothing to answer.
 */
export function ArchiveTasks({
  tasks,
  dict,
  locale,
}: {
  tasks: NonNullable<ArchiveContest["tasks"]>;
  dict: Pick<Dictionary, "archive">;
  locale: Locale;
}) {
  const t = dict.archive.tasks;
  const hasStory = tasks.storyMd.trim() !== "";
  const hasNotes = tasks.schemaNotesMd.trim() !== "";

  return (
    <section aria-labelledby="archive-tasks" className="flex flex-col gap-8">
      <h2 id="archive-tasks" className="text-h3 text-ink">
        {t.heading}
      </h2>

      {hasStory ? (
        <div className="flex flex-col gap-3">
          <h3 className="font-mono text-label text-ink-3 uppercase">{t.story}</h3>
          <StoryText markdown={tasks.storyMd} className="max-w-body" />
        </div>
      ) : null}

      {hasNotes ? (
        <div className="flex flex-col gap-3">
          <h3 className="font-mono text-label text-ink-3 uppercase">{t.notes}</h3>
          <StoryText markdown={tasks.schemaNotesMd} className="max-w-body" />
        </div>
      ) : null}

      {tasks.questions.length > 0 ? (
        <div className="flex flex-col gap-6">
          <h3 className="font-mono text-label text-ink-3 uppercase">{t.questions}</h3>
          {tasks.questions.map((question) => (
            <article key={question.position} className="flex flex-col gap-3 border-t border-line pt-5">
              <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-2">
                <h4 className="text-row text-ink">
                  <span className="font-mono text-data text-ink-3">{question.position}</span>{" "}
                  {question.title}
                </h4>
                <span className="flex items-center gap-2 font-mono text-data text-ink-3">
                  {question.isBonus ? <Tag tone="good">{t.bonus}</Tag> : null}
                  {plural(t.points, question.points, locale)}
                </span>
              </div>
              <StoryText markdown={question.bodyMd} className="max-w-body" />
              {question.choiceIds.length > 0 ? (
                <ul className="flex max-w-body list-disc flex-col gap-1.5 pl-5 text-body text-ink-2">
                  {question.choiceIds.flatMap((id) => {
                    const label = question.choices?.[id];
                    return label === undefined ? [] : [<li key={id}>{label}</li>];
                  })}
                </ul>
              ) : null}
            </article>
          ))}
        </div>
      ) : null}
    </section>
  );
}
