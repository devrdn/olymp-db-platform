import type { Dictionary } from "@/lib/i18n/dictionary";

import { saveStoryAction } from "./actions";
import { PerLanguageForm } from "./per-language-form";

/** The story, one box per language (`per-language-form.tsx`). */
export function StoryEditor({
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
  editable: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.story;
  return (
    <PerLanguageForm
      contestId={contestId}
      name="body"
      languages={languages}
      defaultLanguage={defaultLanguage}
      translations={translations}
      editable={editable}
      action={saveStoryAction}
      labels={{
        fallback: t.fallback,
        placeholder: t.placeholder,
        expand: t.expand,
        collapse: t.collapse,
        unavailable: t.unavailable,
        save: t.save,
        saving: t.saving,
        saved: t.saved,
      }}
      errors={dict.errors}
    />
  );
}
