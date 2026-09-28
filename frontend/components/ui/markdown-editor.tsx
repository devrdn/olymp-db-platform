"use client";

import type { Crepe } from "@milkdown/crepe";
import { Maximize2, Minimize2 } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";

import { cn } from "@/lib/utils";


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
  labels,
}: {
  /** The form field the Markdown is submitted under. */
  name: string;
  defaultValue: string;
  placeholder?: string;
  /**
   * What the one toggle says in each of its two states, and what to say when
   * the editor could not be loaded at all.
   */
  labels: { expand: string; collapse: string; unavailable: string };
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

  // Whether the editor itself could be loaded. Not a detail: its bundle
  // contains a regular expression with a lookbehind and a class static block,
  // both of which are syntax rather than behaviour, so a browser that lacks
  // them throws while parsing and nothing in the chunk ever runs. Safari
  // learned both in 16.4; before that the box rendered, stayed empty, and its
  // own controls did nothing — a silent failure with no way to tell it from a
  // bug of ours.
  //
  // Neither can be transpiled away — there is no downlevel form of a
  // lookbehind — so the editor has to be allowed not to arrive.
  const [unavailable, setUnavailable] = useState(false);

  useEffect(() => {
    if (!host.current) return;

    let live = true;
    let started: Crepe | null = null;

    // Imported here rather than at the top, so that a chunk which cannot be
    // parsed becomes a rejected promise instead of a page that half works.
    void Promise.all([import("@milkdown/crepe"), import("./paste-as-markdown")])
      .then(([{ Crepe }, { pasteAsMarkdown }]) => {
        if (!live || !host.current) return;
        started = build(Crepe, host.current, initial.current, pasteAsMarkdown, setMarkdown);
        return started.create().then(() => {
          if (!live) return void started?.destroy();
          editor.current = started;
        });
      })
      .catch(() => {
        if (live) setUnavailable(true);
      });

    return () => {
      live = false;
      editor.current = null;
      void started?.destroy();
    };
  }, []);

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
        // `isolate` on every editor, expanded or not, and it is what makes the
        // expanded one actually cover the others.
        //
        // Crepe's own stylesheet raises parts of itself a long way: a code
        // block's chrome sits at z-index 999, a table's controls at 100 and
        // 50. `.milkdown` has no z-index of its own, so none of that is
        // contained — it all competes in the page's root stacking context and
        // paints straight through an overlay at 40. Chasing the number would
        // mean chasing a dependency's internals; isolating each editor gives
        // its z-indexes a ceiling of their own instead, and the wrappers then
        // compete on their own terms.
        "isolate flex flex-col",
        // Fixed rather than re-rendered somewhere else: the node the editor is
        // bound to keeps its place in the tree and only moves on screen.
        full && "fixed inset-0 z-40 bg-bg p-4 narrow:p-8",
      )}
    >
      <div className="flex justify-end pb-1.5" hidden={unavailable}>
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

      {unavailable ? (
        // The story is Markdown, so a plain field is the whole document and
        // not a degraded view of it: what is lost is seeing it laid out, not
        // being able to write it. Said out loud, because an author who is not
        // told will spend the afternoon wondering what happened to the
        // formatting buttons.
        <>
          <p role="status" className="pb-1.5 text-small text-ink-2">
            {labels.unavailable}
          </p>
          <textarea
            name={name}
            // The prop, not the captured ref: a textarea is uncontrolled, so
            // React reads this once and a later prop would be ignored anyway.
            defaultValue={defaultValue}
            placeholder={placeholder}
            spellCheck={false}
            className="min-h-40 w-full resize-y border border-edge bg-bg p-3 font-mono text-body text-ink outline-none focus-visible:border-accent narrow:h-[32rem]"
          />
        </>
      ) : null}
      <div
        hidden={unavailable}
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
          //
          // `hidden` rather than `clip`, and they are the same thing here: the
          // spec computes `clip` to `hidden` whenever the other axis scrolls,
          // which this one does. Spelling the used value means one less
          // dependency on how new a browser is, in a component that already
          // behaves differently in more of them than it should.
          "border border-edge bg-bg py-2.5 pr-3 pl-14",
          // Filling the screen means filling it: the box takes the height it
          // has been given and the text scrolls inside, rather than the page
          // scrolling under a surface meant to be the whole of it.
          full
            ? "min-h-0 flex-1 overflow-x-hidden overflow-y-auto py-3.5 pl-24"
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
              "min-h-40 narrow:h-[32rem] narrow:overflow-x-hidden narrow:overflow-y-auto narrow:pl-24",
        )}
      />
      {/* The field the form sends, when the editor is the one holding the
          document. With the fallback the textarea carries the name itself, so
          there is never a second field under it. */}
      {unavailable ? null : <input type="hidden" name={name} value={markdown} />}
    </div>
  );
}

/**
 * Builds the editor once its module has arrived.
 *
 * At module scope and taking the class as an argument, because the import that
 * produces it is deliberately dynamic: a bundle that cannot be parsed has to
 * become a rejected promise rather than a page that half works.
 */
function build(
  Crepe: typeof import("@milkdown/crepe").Crepe,
  root: HTMLElement,
  defaultValue: string,
  pasteAsMarkdown: typeof import("./paste-as-markdown").pasteAsMarkdown,
  onChange: (markdown: string) => void,
) {
  const crepe = new Crepe({
    root,
    defaultValue,
    features: {
      // Off deliberately. Images are governed by SPEC 10.1 and are not
      // uploaded here; LaTeX is not a thing a crime story needs; and an
      // assistant feature would send an author's draft somewhere this
      // installation does not control.
      [Crepe.Feature.ImageBlock]: false,
      [Crepe.Feature.Latex]: false,
      [Crepe.Feature.AI]: false,
      // And the code block's own editor, which crashes.
      //
      // It embeds CodeMirror inside the document, and the two keep separate
      // ideas of where the caret is. When they disagree, ProseMirror maps a
      // selection into the block and CodeMirror refuses it —
      // `RangeError: Selection points outside of document`, thrown from
      // `readDOMChange` while somebody is typing. It is the dependency's bug
      // and not one this side can guard against: the position is computed and
      // applied entirely inside it.
      //
      // Nothing is lost that this product wanted. A crime story does not need
      // a syntax-highlighted editor, and a fenced block is still a fenced
      // block — written, saved and rendered as Markdown, just edited as the
      // plain text it already is. The SQL of an olympiad is typed in the
      // console, which is a real editor for exactly that.
      [Crepe.Feature.CodeMirror]: false,
    },
    featureConfigs: {
      [Crepe.Feature.BlockEdit]: {
        blockHandle: {
          // Pinned to the left, with no middleware. Crepe's default flips the
          // handle to the right of the block when it decides there is not
          // enough room on the left — and a control that changes sides is one
          // somebody has to look for. It also lands past the text, which is
          // where the stray horizontal scroll bar came from.
          getPlacement: () => "left",
          middleware: [],
          // Closer than the default 16, so the pair of controls reaches 74px
          // rather than 82 and fits the gutter with room to spare.
          getOffset: () => 8,
        },
      },
    },
  });

  // The document is Markdown; a paste should be too.
  crepe.editor.use(pasteAsMarkdown);
  crepe.on((api) => api.markdownUpdated((_ctx, value) => onChange(value)));

  return crepe;
}
