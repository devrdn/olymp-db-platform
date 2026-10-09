"use client";

import { useActionState } from "react";

import { StoryText } from "@/components/product/story-text";
import { Button } from "@/components/ui/button";
import { MarkdownEditor } from "@/components/ui/markdown-editor";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { saveStoryAction, type StoryState } from "./actions";
import { messageForCode } from "@/lib/i18n/errors";

/**
 * The story, one box per language side by side, so a translator can compare
 * paragraphs; they stack below the breakpoint. Submitted as a set, which the
 * endpoint replaces whole. Readers see it through `StoryText`, where the
 * security boundary lives.
 */
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
  /**
   * Content freezes at the start. The API refuses edits anyway; this makes the
   * refusal visible first.
   */
  editable: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.story;
  const [state, formAction, pending] = useActionState<StoryState, FormData>(saveStoryAction, {});

  const failure = state.code
    ? (messageForCode(state.code, dict.errors))
    : null;

  return (
    <form action={formAction} className="flex flex-col gap-8">
      <input type="hidden" name="contestId" value={contestId} />

      <div className="grid gap-8 narrow:grid-cols-2">
        {languages.map((lang) => (
          <div key={lang} className="flex flex-col gap-2.5">
            <p className="flex items-baseline gap-2">
              <span className="font-mono text-label text-ink uppercase">{lang}</span>
              {lang === defaultLanguage ? (
                <span className="font-mono text-label text-ink-3 lowercase">{t.fallback}</span>
              ) : null}
            </p>

            {editable ? (
              <MarkdownEditor
                name={`body.${lang}`}
                defaultValue={translations[lang] ?? ""}
                placeholder={t.placeholder}
                labels={{ expand: t.expand, collapse: t.collapse, unavailable: t.unavailable }}
              />
            ) : (
              // Frozen: render it exactly as participants see it, with the same
              // component.
              <StoryText
                markdown={translations[lang] ?? ""}
                className="max-w-narrative border border-line p-4"
              />
            )}
          </div>
        ))}
      </div>

      <div className="flex flex-wrap items-center gap-4">
        <Button type="submit" disabled={pending || !editable}>
          {pending ? t.saving : t.save}
        </Button>

        {/* `status`, not `alert`: a successful save is not an interruption. */}
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
  );
}
