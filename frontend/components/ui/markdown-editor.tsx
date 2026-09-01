"use client";

import { Crepe } from "@milkdown/crepe";
import { Maximize2, Minimize2 } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";

import { cn } from "@/lib/utils";

import { pasteAsMarkdown } from "./paste-as-markdown";

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
 *
 * It can be filled to the screen. Writing a story is the one thing in the
 * constructor that is a long sitting rather than a form to fill, and the
 * column it shares with the other languages is right for comparing them and
 * wrong for writing one. Expanding moves nothing: the same node stays in the
 * same place in the tree and only its position changes, because the editor is
 * a ProseMirror instance bound to that node and a second copy would lose the
 * undo history and whatever was typed into the one being discarded.
 */
export function MarkdownEditor({
  name,
  defaultValue,
  placeholder,
  readOnly,
  className,
  labels,
}: {
  /** The form field the Markdown is submitted under. */
  name: string;
  defaultValue: string;
  placeholder?: string;
  readOnly?: boolean;
  className?: string;
  /** What the one toggle says, in each of its two states. */
  labels: { expand: string; collapse: string };
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
      featureConfigs: {
        [Crepe.Feature.BlockEdit]: {
          blockHandle: {
            // Pinned to the left, with no middleware. Crepe's default flips
            // the handle to the right of the block when it decides there is
            // not enough room on the left — and a control that changes sides
            // is one somebody has to look for. It also lands past the text,
            // which is where the stray horizontal scroll bar came from.
            getPlacement: () => "left",
            middleware: [],
            // Closer than the default 16, so the pair of controls reaches
            // 74px rather than 82 and fits the gutter with room to spare.
            getOffset: () => 8,
          },
        },
      },
    });

    // The document is Markdown; a paste should be too.
    crepe.editor.use(pasteAsMarkdown);

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

  const [full, setFull] = useState(false);

  // The page behind must not scroll under a surface that covers it, or closing
  // leaves the author somewhere they never went. Cleared on unmount too:
  // navigating away from an expanded editor must not leave a page that cannot
  // scroll and no control left to fix it.
  useEffect(() => {
    if (!full) return;

    const previous = document.body.style.overflow;
    document.body.style.overflow = "hidden";

    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") setFull(false);
    };
    document.addEventListener("keydown", onKey);

    return () => {
      document.body.style.overflow = previous;
      document.removeEventListener("keydown", onKey);
    };
  }, [full]);

  const toggle = useCallback(() => setFull((open) => !open), []);

  return (
    <div
      className={cn(
        "flex flex-col",
        // Fixed rather than re-rendered somewhere else: the node the editor is
        // bound to keeps its place in the tree and only moves on screen.
        full && "fixed inset-0 z-40 bg-bg p-4 narrow:p-8",
        className,
      )}
    >
      <div className="flex justify-end pb-1.5">
        <button
          type="button"
          onClick={toggle}
          aria-expanded={full}
          title={full ? labels.collapse : labels.expand}
          aria-label={full ? labels.collapse : labels.expand}
          className="grid size-7 place-items-center rounded-full text-ink-3 transition-colors duration-(--t-input) ease-standard hover:bg-sunk hover:text-ink"
        >
          {full ? (
            <Minimize2 className="size-4" strokeWidth={1.75} aria-hidden />
          ) : (
            <Maximize2 className="size-4" strokeWidth={1.75} aria-hidden />
          )}
        </button>
      </div>

      <div
        ref={host}
        data-editor-host
        data-placeholder={placeholder}
        className={cn(
          // The handle is two 32px controls — add a block, and drag it —
          // separated by 2px and 8px clear of the paragraph, so it reaches
          // 74px to the left of where the text starts. It is positioned
          // against `.milkdown`, which begins at this box's content edge, so
          // the gutter is what decides whether it lands inside: 56px where the
          // box may overflow visibly, 96px wherever the box clips.
          //
          // Clipping belongs on this element and never on `.milkdown` —
          // putting it there clips the handle itself, since the handle sits at
          // a negative offset from exactly that box.
          "border border-edge bg-bg py-2.5 pr-3 pl-14",
          // Filling the screen means filling it: the box takes the height it
          // has been given and the text scrolls inside, rather than the page
          // scrolling under a surface meant to be the whole of it.
          full
            ? "min-h-0 flex-1 overflow-x-clip overflow-y-auto py-3.5 pl-24"
            : // Closed, every language is the same rectangle, and the text
              // scrolls inside it. A height that follows the content leaves
              // the row of languages a staircase — one box short, the next
              // twice as tall, a scroll bar on whichever happens to be
              // longest — and it is the same story in three languages, so
              // they should look like three of the same thing. It also stops
              // a long story pushing everything below it a thousand pixels
              // down; writing at length is what the expand control is for.
              //
              // From the one breakpoint up, and not on a phone: there a box
              // that scrolls inside a page that scrolls is a trap for the
              // thumb, and the 96px of gutter that clipping costs would take
              // a quarter of the screen away from the words.
              "min-h-40 narrow:h-[32rem] narrow:overflow-x-clip narrow:overflow-y-auto narrow:pl-24",
        )}
      />
      <input type="hidden" name={name} value={markdown} />
    </div>
  );
}
