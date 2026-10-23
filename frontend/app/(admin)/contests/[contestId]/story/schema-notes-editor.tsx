import { useId } from "react";

import { Tag } from "@/components/ui/tag";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { saveSchemaNotesAction } from "./actions";
import { PerLanguageForm } from "./per-language-form";

/**
 * The table descriptions, below the story and laid out like it
 * (`per-language-form.tsx`). Participants read them through `StoryText` on a
 * Tables tab of their own.
 */
export function SchemaNotesEditor({
  contestId,
  languages,
  defaultLanguage,
  translations,
  editable,
  dict,
}: {
  contestId: string;
  languages: string[];
  defaultLanguage?: string;
  translations: Record<string, string>;
  /** Frozen with the story at the start; the API refuses edits anyway. */
  editable: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.schemaNotes;
  const story = dict.workspace.story;
  const headingId = useId();

  return (
    <section aria-labelledby={headingId} className="flex flex-col gap-8 border-t border-line pt-8">
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-3">
          <h2 id={headingId} className="text-h3 text-ink">
            {t.heading}
          </h2>
          {!editable ? <Tag tone="mute">{dict.workspace.facts.frozen}</Tag> : null}
        </div>
        <p className="max-w-body text-body text-ink-2">{t.lede}</p>
        <p className="max-w-body text-small text-ink-3">{t.catalogueHint}</p>
      </div>

      <PerLanguageForm
        contestId={contestId}
        name="schemaNotes"
        languages={languages}
        defaultLanguage={defaultLanguage}
        translations={translations}
        editable={editable}
        action={saveSchemaNotesAction}
        labels={{
          fallback: story.fallback,
          placeholder: t.placeholder,
          expand: story.expand,
          collapse: story.collapse,
          unavailable: t.unavailable,
          save: t.save,
          saving: t.saving,
          saved: t.saved,
          empty: t.empty,
        }}
        errors={dict.errors}
      />
    </section>
  );
}
