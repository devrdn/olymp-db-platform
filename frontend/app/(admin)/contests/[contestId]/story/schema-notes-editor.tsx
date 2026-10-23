"use client";

import { useActionState, useId } from "react";

import { StoryText } from "@/components/product/story-text";
import { Button } from "@/components/ui/button";
import { MarkdownEditor } from "@/components/ui/markdown-editor";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { messageForCode } from "@/lib/i18n/errors";

import { saveSchemaNotesAction, type SchemaNotesState } from "./actions";

/**
 * The table descriptions, laid out like the story above them: one box per
 * language, submitted as a set the endpoint replaces whole. Participants read
 * them through `StoryText` on a Tables tab of their own.
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
  const [state, formAction, pending] = useActionState<SchemaNotesState, FormData>(saveSchemaNotesAction, {});
  const headingId = useId();

  const failure = state.code ? messageForCode(state.code, dict.errors) : null;

  return (
    <section aria-labelledby={headingId} className="flex flex-col gap-8 border-t border-line pt-8">
      <div className="flex flex-col gap-3">
        <h2 id={headingId} className="text-h3 text-ink">
          {t.heading}
        </h2>
        <p className="max-w-body text-body text-ink-2">{t.lede}</p>
        <p className="max-w-body text-small text-ink-3">{t.catalogueHint}</p>
      </div>

      <form action={formAction} className="flex flex-col gap-8">
        <input type="hidden" name="contestId" value={contestId} />

        <div className="grid gap-8 narrow:grid-cols-2">
          {languages.map((lang) => (
            <div key={lang} className="flex flex-col gap-2.5">
              <p className="flex items-baseline gap-2">
                <span className="font-mono text-label text-ink uppercase">{lang}</span>
                {lang === defaultLanguage ? (
                  <span className="font-mono text-label text-ink-3 lowercase">{dict.workspace.story.fallback}</span>
                ) : null}
              </p>

              {editable ? (
                <MarkdownEditor
                  name={`schemaNotes.${lang}`}
                  defaultValue={translations[lang] ?? ""}
                  placeholder={t.placeholder}
                  labels={{
                    expand: dict.workspace.story.expand,
                    collapse: dict.workspace.story.collapse,
                    unavailable: t.unavailable,
                  }}
                />
              ) : (
                <StoryText markdown={translations[lang] ?? ""} className="max-w-narrative border border-line p-4" />
              )}
            </div>
          ))}
        </div>

        <div className="flex flex-wrap items-center gap-4">
          <Button type="submit" disabled={pending || !editable}>
            {pending ? t.saving : t.save}
          </Button>

          {state.saved ? (
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
      </form>
    </section>
  );
}
