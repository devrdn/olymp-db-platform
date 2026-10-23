"use client";

import { useActionState } from "react";

import { StoryText } from "@/components/product/story-text";
import { Button } from "@/components/ui/button";
import { MarkdownEditor } from "@/components/ui/markdown-editor";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { messageForCode } from "@/lib/i18n/errors";

import type { TranslationsState } from "./actions";

export type PerLanguageLabels = {
  /** Marks the contest's default language. */
  fallback: string;
  placeholder: string;
  expand: string;
  collapse: string;
  unavailable: string;
  save: string;
  saving: string;
  saved: string;
  /** Shown in place of a frozen language with no text; omitted shows the empty box. */
  empty?: string;
};

/**
 * One Markdown document per language, side by side so a translator can compare
 * paragraphs; they stack below the breakpoint. Submitted as a set under
 * `{name}.{lang}`, which the endpoint replaces whole. Readers see it through
 * `StoryText`, where the security boundary lives, and so does this form once
 * the content is frozen.
 */
export function PerLanguageForm({
  contestId,
  name,
  languages,
  defaultLanguage,
  translations,
  editable,
  action,
  labels,
  errors,
}: {
  contestId: string;
  /** The field prefix the action reads. */
  name: string;
  languages: string[];
  defaultLanguage?: string;
  translations: Record<string, string>;
  /**
   * Content freezes at the start. The API refuses edits anyway; this makes the
   * refusal visible first.
   */
  editable: boolean;
  action: (previous: TranslationsState, form: FormData) => Promise<TranslationsState>;
  labels: PerLanguageLabels;
  errors: Dictionary["errors"];
}) {
  const [state, formAction, pending] = useActionState<TranslationsState, FormData>(action, {});

  const failure = state.code ? messageForCode(state.code, errors) : null;

  return (
    <form action={formAction} className="flex flex-col gap-8">
      <input type="hidden" name="contestId" value={contestId} />

      <div className="grid gap-8 narrow:grid-cols-2">
        {languages.map((lang) => (
          <div key={lang} className="flex flex-col gap-2.5">
            <p className="flex items-baseline gap-2">
              <span className="font-mono text-label text-ink uppercase">{lang}</span>
              {lang === defaultLanguage ? (
                <span className="font-mono text-label text-ink-3 lowercase">{labels.fallback}</span>
              ) : null}
            </p>

            {editable ? (
              <MarkdownEditor
                name={`${name}.${lang}`}
                defaultValue={translations[lang] ?? ""}
                placeholder={labels.placeholder}
                labels={{ expand: labels.expand, collapse: labels.collapse, unavailable: labels.unavailable }}
              />
            ) : labels.empty !== undefined && !translations[lang]?.trim() ? (
              <p className="text-body text-ink-3">{labels.empty}</p>
            ) : (
              <StoryText markdown={translations[lang] ?? ""} className="max-w-narrative border border-line p-4" />
            )}
          </div>
        ))}
      </div>

      <div className="flex flex-wrap items-center gap-4">
        <Button type="submit" disabled={pending || !editable}>
          {pending ? labels.saving : labels.save}
        </Button>

        {/* `status`, not `alert`: a successful save is not an interruption. */}
        {state.saved ? (
          <p role="status" className="text-small text-good">
            {labels.saved}
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
