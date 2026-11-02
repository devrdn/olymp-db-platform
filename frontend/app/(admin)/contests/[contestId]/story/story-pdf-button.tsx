"use client";

import { FileDown } from "lucide-react";
import { useId, useState } from "react";

import { Button } from "@/components/ui/button";
import { saveBlob } from "@/lib/download";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { storyFileName } from "./pdf/file-name";

export type StoryPdfLabels = Pick<
  Dictionary["workspace"]["story"],
  "pdf" | "pdfMaking" | "pdfUnsaved" | "pdfEmpty" | "pdfFailed"
>;

/**
 * Downloads one language's saved story as a PDF file.
 *
 * The file is made here, in the administrator's browser, and the library that
 * makes it is fetched on the first click: nothing of it is part of this page's
 * own code, and no participant screen reaches it.
 */
export function StoryPdfButton({
  title,
  language,
  markdown,
  logoHref,
  unsaved,
  labels,
}: {
  title: string;
  language: string;
  /** The saved text, never the editor's. */
  markdown: string;
  /** The installation's uploaded logo; none means the product mark. */
  logoHref?: string;
  /** Whether the editor holds text that is not saved yet. */
  unsaved: boolean;
  labels: StoryPdfLabels;
}) {
  const [making, setMaking] = useState(false);
  const [failed, setFailed] = useState(false);
  const hintId = useId();

  const empty = markdown.trim() === "";
  const hint = unsaved ? labels.pdfUnsaved : empty ? labels.pdfEmpty : null;
  const visible = making ? labels.pdfMaking : labels.pdf;

  async function download() {
    setMaking(true);
    setFailed(false);
    try {
      const { makeStoryPdf } = await import("./pdf/make-story-pdf");
      const blob = await makeStoryPdf({ title, language, markdown, logoHref });
      saveBlob(blob, storyFileName(title, language));
    } catch {
      setFailed(true);
    } finally {
      setMaking(false);
    }
  }

  return (
    <div className="flex flex-col items-end gap-1.5">
      <Button
        type="button"
        variant="secondary"
        size="sm"
        onClick={download}
        disabled={making || unsaved || empty}
        aria-busy={making}
        // Begins with the visible words, as WCAG 2.5.3 asks; the language says which of the page's buttons this is.
        aria-label={`${visible}, ${language}`}
        aria-describedby={hint ? hintId : undefined}
      >
        <FileDown strokeWidth={1.75} aria-hidden />
        {visible}
      </Button>

      {hint ? (
        <p id={hintId} className="max-w-body text-right text-small text-ink-3">
          {hint}
        </p>
      ) : null}

      {failed ? (
        <p role="alert" className="max-w-body text-right text-small text-bad">
          {labels.pdfFailed}
        </p>
      ) : null}
    </div>
  );
}
