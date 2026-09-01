"use client";

import { useActionState } from "react";

import { StoryText } from "@/components/product/story-text";
import { Button } from "@/components/ui/button";
import { MarkdownEditor } from "@/components/ui/markdown-editor";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { saveStoryAction, type StoryState } from "./actions";

/**
 * The crime story, one box per declared language.
 *
 * Side by side rather than behind tabs. A translator's actual question is
 * "does this paragraph say what the English one says", and a tab hides the
 * sentence they are comparing against. Below the layout breakpoint they stack,
 * which is the same order at a different width.
 *
 * The whole set is submitted together, because the endpoint replaces it as a
 * set and the publish gate reasons about it as one.
 *
 * Formatting is shown where it is typed rather than in a pane underneath: a
 * heading is large, bold is bold, a table is a table. What is stored is still
 * Markdown, so prepared Markdown can be pasted in and what comes back out is
 * Markdown somebody could edit by hand.
 *
 * What a reader finally sees is rendered by `StoryText`, not by this editor —
 * that separation is what keeps the security boundary on the reading side
 * where it belongs (see `components/ui/markdown-editor.tsx`).
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
  /** Content freezes at the start: changing a question under someone answering
   *  it is changing their task. The API refuses either way; this is so the
   *  refusal is visible before it is provoked. */
  editable: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.story;
  const [state, formAction, pending] = useActionState<StoryState, FormData>(saveStoryAction, {});

  const failure = state.code
    ? ((dict.errors as Record<string, string>)[state.code] ?? dict.errors.fallback)
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
              />
            ) : (
              // A frozen contest gets the story as a reader sees it, rendered
              // by the same component the participant's screen uses. A
              // read-only editor would be chrome around text nobody may
              // change, and it would show the author something subtly other
              // than what is being read right now.
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

        {/* `status`, not `alert`: a save that worked is not an interruption. */}
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
