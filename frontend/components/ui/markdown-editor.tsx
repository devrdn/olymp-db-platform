"use client";

import type { Crepe } from "@milkdown/crepe";
import { Maximize2, Minimize2 } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";

import { cn } from "@/lib/utils";


import "@milkdown/crepe/theme/common/style.css";
import "./markdown-editor.css";

/**
 * A WYSIWYG editor whose stored document is Markdown (Milkdown Crepe).
 *
 * Every edit round-trips the document tree to Markdown, so anything the tree
 * cannot hold is dropped; for the story's prose that is only raw HTML, which
 * the reader refuses anyway. This is not the security boundary: participants
 * read the story through `StoryText`, which never builds an HTML string.
 *
 * Expanding to full screen only repositions the same node: the editor is
 * bound to it, and a second copy would lose the undo history.
 */
export function MarkdownEditor({
  name,
  defaultValue,
  placeholder,
  labels,
}: {
  name: string;
  defaultValue: string;
  placeholder?: string;
  /** The expand toggle's two labels, and the message shown when the editor cannot load. */
  labels: { expand: string; collapse: string; unavailable: string };
}) {
  const host = useRef<HTMLDivElement>(null);
  const editor = useRef<Crepe | null>(null);
  // Held in state rather than read from the editor at submit, so the field is
  // right while the editor initialises or after it is torn down.
  const [markdown, setMarkdown] = useState(defaultValue);

  // Captured once: rebuilding the editor on a prop change would tear it down
  // under the caret.
  const initial = useRef(defaultValue);

  // The editor's bundle uses lookbehind regexes and class static blocks, syntax
  // that cannot be transpiled away; a browser without them (Safari before 16.4)
  // fails to parse the chunk, so the editor must be allowed not to arrive.
  const [unavailable, setUnavailable] = useState(false);

  useEffect(() => {
    if (!host.current) return;

    let live = true;
    let started: Crepe | null = null;

    // Dynamic, so a chunk that cannot be parsed becomes a rejected promise
    // instead of a page that half works.
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

  // Lock page scroll while expanded, and release it on unmount so navigating
  // away never leaves the page unscrollable.
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
        // `isolate` contains Crepe's own z-indexes (up to 999), which would
        // otherwise paint through the expanded overlay at 40.
        "isolate flex flex-col",
        // Fixed rather than re-rendered elsewhere, so the editor's node keeps
        // its place in the tree.
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
        // A plain field is still the whole Markdown document; say so, so the
        // author does not wonder where the formatting buttons went.
        <>
          <p role="status" className="pb-1.5 text-small text-ink-2">
            {labels.unavailable}
          </p>
          <textarea
            name={name}
            // The prop, not the ref: an uncontrolled textarea reads it once
            // anyway.
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
          // The block handle reaches 74px left of the text and is positioned
          // against `.milkdown`, so the left padding decides whether it lands
          // inside: 56px when the box may overflow, 96px where it clips. Clip
          // here, never on `.milkdown`, or the handle itself is clipped.
          // `hidden` equals `clip` here because the other axis scrolls.
          "border border-edge bg-bg py-2.5 pr-3 pl-14",
          // Expanded, the text scrolls inside the box rather than the page
          // scrolling under it.
          full
            ? "min-h-0 flex-1 overflow-x-hidden overflow-y-auto py-3.5 pl-24"
            : // Closed, every language gets the same fixed height so the three
              // boxes line up. Not on a phone, where a scrolling box inside a
              // scrolling page traps the thumb.
              "min-h-40 narrow:h-[32rem] narrow:overflow-x-hidden narrow:overflow-y-auto narrow:pl-24",
        )}
      />
      {/* With the fallback, the textarea carries the name, so there is never a second field. */}
      {unavailable ? null : <input type="hidden" name={name} value={markdown} />}
    </div>
  );
}

/** Takes the class as an argument because its import is dynamic. */
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
      // Images are not uploaded here, LaTeX is not needed, and an assistant
      // would send drafts to a service this installation does not control.
      [Crepe.Feature.ImageBlock]: false,
      [Crepe.Feature.Latex]: false,
      [Crepe.Feature.AI]: false,
      // The code block's embedded CodeMirror crashes (`RangeError: Selection
      // points outside of document`) when it and ProseMirror disagree about the
      // caret, inside the dependency where this side cannot guard. A fenced
      // block is still edited as plain text.
      [Crepe.Feature.CodeMirror]: false,
    },
    featureConfigs: {
      [Crepe.Feature.BlockEdit]: {
        blockHandle: {
          // Pinned left: Crepe's default flips the handle to the right when
          // space is short, where it overflows the text.
          getPlacement: () => "left",
          middleware: [],
          // Default is 16; 8 keeps the handle within the gutter (74px rather
          // than 82).
          getOffset: () => 8,
        },
      },
    },
  });

  crepe.editor.use(pasteAsMarkdown);
  crepe.on((api) => api.markdownUpdated((_ctx, value) => onChange(value)));

  return crepe;
}
