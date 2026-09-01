"use client";

import { Crepe } from "@milkdown/crepe";
import { useEffect, useRef, useState } from "react";

import "@milkdown/crepe/theme/common/style.css";
import "./markdown-editor.css";

/**
 * A what-you-see editor whose document is Markdown.
 *
 * Formatting is rendered where it is typed: a heading is large, bold text is
 * bold, a table is a table. What is stored is still Markdown — the editor
 * parses it in and serialises it out, so prepared Markdown can be pasted in
 * and what comes back is Markdown a person could edit by hand.
 *
 * The honest cost, stated once. A rich editor holds a document tree, and every
 * keystroke round-trips that tree back to Markdown. Anything the tree cannot
 * represent does not survive the trip. Two things make that acceptable here
 * rather than merely tolerable:
 *
 * - Raw HTML is the usual casualty of a round trip, and this application
 *   forbids it anyway. What the editor drops is what the reader would have
 *   refused to render (see `components/product/story-text.tsx`).
 * - The story is the only thing edited this way, and it is prose: paragraphs,
 *   headings, emphasis, lists, quotes, tables. All of it is in the tree.
 *
 * The security boundary is not here. This renders an author's own text to that
 * same author, which is nobody else's problem; what every participant reads is
 * rendered by `StoryText`, which never builds an HTML string. Anything this
 * editor let through would still arrive at a reader as text.
 *
 * One consequence worth knowing: the story screen now needs JavaScript. The
 * rest of the constructor does not, and the plain forms elsewhere were kept
 * for that reason — but there is no editor of this kind without it.
 */
export function MarkdownEditor({
  name,
  defaultValue,
  placeholder,
  readOnly,
  className,
}: {
  /** The form field the Markdown is submitted under. */
  name: string;
  defaultValue: string;
  placeholder?: string;
  readOnly?: boolean;
  className?: string;
}) {
  const host = useRef<HTMLDivElement>(null);
  const editor = useRef<Crepe | null>(null);
  // The value the form will send. Held in state rather than read out of the
  // editor at submit time, so the field is correct even if the editor is
  // still initialising or has been torn down.
  const [markdown, setMarkdown] = useState(defaultValue);

  // The starting document, captured once. It is not a controlled value:
  // rebuilding the editor whenever the prop changed would tear it down under
  // the caret on every keystroke.
  const initial = useRef(defaultValue);

  useEffect(() => {
    if (!host.current) return;

    const crepe = new Crepe({
      root: host.current,
      defaultValue: initial.current,
      features: {
        // Off deliberately. Images are governed by SPEC 10.1 and are not
        // uploaded here; LaTeX is not a thing a crime story needs; and an
        // assistant feature would send an author's draft somewhere this
        // installation does not control.
        [Crepe.Feature.ImageBlock]: false,
        [Crepe.Feature.Latex]: false,
        [Crepe.Feature.AI]: false,
      },
    });

    crepe.on((api) => api.markdownUpdated((_ctx, value) => setMarkdown(value)));

    let live = true;
    crepe.create().then(() => {
      if (!live) return void crepe.destroy();
      editor.current = crepe;
    });

    return () => {
      live = false;
      editor.current = null;
      void crepe.destroy();
    };
  }, []);

  // Separately, because it can change while the page is open: a contest that
  // starts freezes its content, and an editor that stayed writable would let
  // somebody type into a story the API will refuse to save.
  useEffect(() => {
    editor.current?.setReadonly(Boolean(readOnly));
  }, [readOnly]);

  return (
    <div className={className}>
      <div
        ref={host}
        data-placeholder={placeholder}
        className="min-h-40 border border-edge bg-bg px-3 py-2.5"
      />
      <input type="hidden" name={name} value={markdown} />
    </div>
  );
}
