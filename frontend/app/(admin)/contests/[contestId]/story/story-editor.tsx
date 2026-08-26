"use client";

import { useActionState } from "react";

import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
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
            <label htmlFor={`body-${lang}`} className="flex items-baseline gap-2">
              <span className="font-mono text-label text-ink uppercase">{lang}</span>
              {lang === defaultLanguage ? (
                <span className="font-mono text-label text-ink-3 lowercase">{t.fallback}</span>
              ) : null}
            </label>

            <Textarea
              id={`body-${lang}`}
              name={`body.${lang}`}
              defaultValue={translations[lang] ?? ""}
              disabled={!editable}
              placeholder={t.placeholder}
            />
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
