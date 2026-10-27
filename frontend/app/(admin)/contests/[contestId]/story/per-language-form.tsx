"use client";

import { useActionState, useState } from "react";

import { StoryText } from "@/components/product/story-text";
import { Button } from "@/components/ui/button";
import { MarkdownEditor } from "@/components/ui/markdown-editor";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { storedMarkdown } from "@/lib/format/markdown";
import { messageForCode } from "@/lib/i18n/errors";

import type { TranslationsState } from "./actions";
import { StoryPdfButton, type StoryPdfLabels } from "./story-pdf-button";

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

/** What a language's box needs to offer the story as a PDF file. */
export type PdfOffer = {
  /** The contest's title in each language. */
  titles: Record<string, string>;
  /** The installation's uploaded logo. */
  logoHref?: string;
  labels: StoryPdfLabels;
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
  pdf,
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
  /** Offers each language's saved text as a PDF file; omitted offers none. */
  pdf?: PdfOffer;
}) {
  const [state, formAction, pending] = useActionState<TranslationsState, FormData>(action, {});

  const failure = state.code ? messageForCode(state.code, errors) : null;

  // What each box holds now, once it has been edited; a language never touched is as saved.
  // It is compared with the editor's own writing of the text it loaded, which can differ from it
  // in form alone (`*em*` against `_em_`), for as long as that is the text saved; after a save the
  // saved text is the editor's own. Both are compared as they would be stored.
  const [edited, setEdited] = useState<Record<string, string>>({});
  const [loaded, setLoaded] = useState<Record<string, { saved: string; written: string }>>({});

  // Freezing removes the editors and thawing builds new ones from the saved text, so what the
  // old ones held or wrote no longer describes anything on the page.
  const [editorsFor, setEditorsFor] = useState(editable);
  if (editorsFor !== editable) {
    setEditorsFor(editable);
    setEdited({});
    setLoaded({});
  }

  const unsaved = (lang: string) => {
    if (!editable || !(lang in edited)) return false;
    const saved = translations[lang] ?? "";
    const baseline = loaded[lang]?.saved === saved ? loaded[lang].written : saved;
    return storedMarkdown(edited[lang]) !== storedMarkdown(baseline);
  };

  return (
    <form action={formAction} className="flex flex-col gap-8">
      <input type="hidden" name="contestId" value={contestId} />

      <div className="grid gap-8 narrow:grid-cols-2">
        {languages.map((lang) => (
          <div key={lang} className="flex flex-col gap-2.5">
            <div className="flex items-start justify-between gap-3">
              <p className="flex items-baseline gap-2">
                <span className="font-mono text-label text-ink uppercase">{lang}</span>
                {lang === defaultLanguage ? (
                  <span className="font-mono text-label text-ink-3 lowercase">{labels.fallback}</span>
                ) : null}
              </p>

              {pdf ? (
                <StoryPdfButton
                  title={pdf.titles[lang] ?? ""}
                  language={lang}
                  markdown={translations[lang] ?? ""}
                  logoHref={pdf.logoHref}
                  unsaved={unsaved(lang)}
                  labels={pdf.labels}
                />
              ) : null}
            </div>

            {editable ? (
              <MarkdownEditor
                name={`${name}.${lang}`}
                defaultValue={translations[lang] ?? ""}
                placeholder={labels.placeholder}
                onChange={(markdown) => setEdited((previous) => ({ ...previous, [lang]: markdown }))}
                onReady={(written, saved) => setLoaded((previous) => ({ ...previous, [lang]: { saved, written } }))}
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
