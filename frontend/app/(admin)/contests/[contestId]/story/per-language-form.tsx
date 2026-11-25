"use client";

import { useState } from "react";

import { ActionFooter, ActionForm, useDraftEdited } from "@/components/product/action-form";
import { StoryText } from "@/components/product/story-text";
import { MarkdownEditor } from "@/components/ui/markdown-editor";
import type { Contest } from "@/lib/api/contests";
import { atVersion } from "@/lib/api/contests-terms";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { storedMarkdown } from "@/lib/format/markdown";

import { ReloadWhenChanged } from "../reload-when-changed";
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
  /** Offered under a save refused because the contest moved since the form was rendered. */
  reload: string;
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
  contest,
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
  contest: Pick<Contest, "id" | "version">;
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
  return (
    <ActionForm action={action} {...atVersion(contest, JSON.stringify(translations))} errors={errors} className="flex flex-col gap-8">
      <PerLanguageFields
        name={name}
        languages={languages}
        defaultLanguage={defaultLanguage}
        translations={translations}
        editable={editable}
        labels={labels}
        pdf={pdf}
      />
    </ActionForm>
  );
}

/**
 * The boxes, inside the form so a reload starts them over, with what they
 * know of their own edits. A built editor whose text now differs from the
 * saved one marks the form edited (`useDraftEdited`); the editor's own
 * rewriting of the text it loaded does not.
 */
function PerLanguageFields({
  name,
  languages,
  defaultLanguage,
  translations,
  editable,
  labels,
  pdf,
}: Omit<Parameters<typeof PerLanguageForm>[0], "contest" | "action" | "errors">) {
  const markEdited = useDraftEdited();
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

  const baseline = (lang: string) => {
    const saved = translations[lang] ?? "";
    return loaded[lang]?.saved === saved ? loaded[lang].written : saved;
  };
  const unsaved = (lang: string) =>
    editable && lang in edited && storedMarkdown(edited[lang]) !== storedMarkdown(baseline(lang));

  return (
    <>
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
                onChange={(markdown) => {
                  setEdited((previous) => ({ ...previous, [lang]: markdown }));
                  // Not before the editor reports itself built, when a change is its own
                  // rewriting of the loaded text: an edit typed that early, unlikely as it
                  // is, does not pin the form's version, and any typed after it does.
                  if (lang in loaded && storedMarkdown(markdown) !== storedMarkdown(baseline(lang))) markEdited();
                }}
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

      <ActionFooter labels={labels} disabled={!editable}>
        <ReloadWhenChanged label={labels.reload} />
      </ActionFooter>
    </>
  );
}
