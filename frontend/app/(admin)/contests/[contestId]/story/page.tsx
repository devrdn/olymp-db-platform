import { Tag } from "@/components/ui/tag";
import { schemaNotesSchema, storySchema } from "@/lib/api/content";
import { branding } from "@/lib/api/branding";
import { contentEditable, defaultLanguage, titleIn } from "@/lib/api/contests";
import { imageHref } from "@/lib/api/settings-terms";
import { activeDictionary } from "@/lib/i18n/server";

import { loadContest, loadContestResource } from "../contest";
import { SchemaNotesEditor } from "./schema-notes-editor";
import { StoryEditor } from "./story-editor";

/**
 * The story, and the table descriptions below it. A 404 for the story means
 * nothing is written yet, so the editor opens empty rather than showing a
 * not-found page; the descriptions answer an empty set instead.
 */
export default async function StoryPage(props: PageProps<"/contests/[contestId]/story">) {
  const [{ contestId }, dict] = await Promise.all([props.params, activeDictionary()]);

  const [contest, story, schemaNotes, brand] = await Promise.all([
    loadContest(contestId),
    loadContestResource(contestId, "/story", (payload) => storySchema.parse(payload), {
      notFoundIsEmpty: true,
    }),
    loadContestResource(contestId, "/schema-notes", (payload) => schemaNotesSchema.parse(payload)),
    branding(),
  ]);

  const t = dict.workspace.story;
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
      </div>

      {languages.length === 0 ? (
        /* No languages yet; they are chosen in Settings. */
        <p className="max-w-body text-body text-ink-3">{t.noLanguages}</p>
      ) : (
        <>
          <StoryEditor
            contestId={contest.id}
            languages={languages}
            defaultLanguage={defaultLanguage(contest)}
            translations={story?.translations ?? {}}
            editable={editable}
            dict={dict}
            pdf={{
              titles: Object.fromEntries(languages.map((code) => [code, titleIn(contest, code)])),
              logoHref: brand.images.logo ? imageHref("logo", brand.images.logo) : undefined,
            }}
          />
          <SchemaNotesEditor
            contestId={contest.id}
            languages={languages}
            defaultLanguage={defaultLanguage(contest)}
            translations={schemaNotes?.translations ?? {}}
            editable={editable}
            dict={dict}
          />
        </>
      )}
    </div>
  );
}
