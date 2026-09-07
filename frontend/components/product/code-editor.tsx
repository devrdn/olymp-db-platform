"use client";

import { useEffect, useLayoutEffect, useRef, useState } from "react";

import { cn } from "@/lib/utils";

import type { EditorView } from "./code-editor-core";

export type CodeEditorProps = {
  /** The editor's accessible name. */
  ariaLabel: string;
  placeholder: string;
  /**
   * Read once, when the real editor is constructed — not a controlled
   * `value`. CodeMirror owns its document from then on; see the file doc
   * comment for why a controlled value would put React back on the typing
   * path. Also read once for the fallback field below, so whichever one is
   * showing when a participant starts typing already has the right text.
   */
  getInitialValue: () => string;
  /**
   * Called on every change to the document — the fallback field's own
   * keystrokes included — with the current text. Must not itself cause a
   * re-render of whatever owns this component — write to a ref or an
   * uncontrolled DOM node, the same contract `ConsoleEditor`'s own former
   * `onInput` already kept. This component enforces its own half: both paths
   * call it from outside React's render cycle (a DOM `input` event, or
   * CodeMirror's own update listener), so nothing here can commit a render
   * no matter what the callback does with the value.
   */
  onChange: (text: string) => void;
  /**
   * A 1-based character offset into the document — PostgreSQL's own
   * convention — or `undefined` for "nothing to point at". Changing this
   * moves the underline; it does not touch the document. Has no effect while
   * the fallback field is showing — there is nothing yet to mark up until the
   * real editor exists, and it re-applies immediately once CodeMirror
   * mounts (see the mount effect below).
   */
  errorPosition?: number;
  /**
   * Changes identity once per run that could have produced `errorPosition` —
   * pass the settled action state itself, or any value that is a *new*
   * reference each time, not just a new number. `errorPosition` alone is not
   * enough: a refusal at the same character as the one before is the same
   * number, so an effect keyed on `errorPosition` never re-fires for it, and
   * the underline that CodeMirror's own error field clears on every edit
   * never comes back (finding 4). Only used to distinguish "another run
   * settled" from "nothing happened" — its value is never read.
   */
  errorToken?: unknown;
  className?: string;
};

/**
 * The SQL editor section 11 names: CodeMirror 6, wearing this product's
 * theme, highlighting the position PostgreSQL says a syntax error is at.
 *
 * # Why this file is not the editor
 *
 * CodeMirror itself — `code-editor-core.ts` — is loaded with a dynamic
 * `import()`, not a static one, because it is not small: `@codemirror/*` plus
 * `@lezer/highlight` measured at roughly 140 KiB gzipped (see the task
 * report). Statically importing it here would put that weight on the play
 * route's own critical path, downloaded and parsed before a participant could
 * do anything at all — including the "Run" button, the timer, or reading the
 * story, none of which need CodeMirror to exist.
 *
 * That would not be the only cost. Before this component's mount effect has
 * run — which needs React to have hydrated *and*, now, the dynamic import to
 * have resolved — there is nothing here for CodeMirror to have mounted into.
 * A naive version of this component would render an empty `<div>` for that
 * whole window, and an empty div cannot be typed into: a participant who
 * starts reading the question and typing a query the instant the page paints
 * — which is exactly what a timed olympiad rewards — would have their first
 * keystrokes go nowhere. That is worse than the plain `<textarea>` this
 * replaces, which was typable from the first paint because it needs no
 * JavaScript to accept text at all.
 *
 * So this component always renders a real, server-rendered `<textarea>`
 * first — typable before hydration, before the chunk arrives, before
 * anything — and swaps to CodeMirror only once `mountEditor` has actually
 * returned a view. Nothing typed into the fallback is lost in the handoff:
 * `getInitialValue` is read again at that moment, and it reads through to the
 * same ref-backed field (`ConsoleEditor`'s mirror) the fallback's own
 * `onChange` already kept current.
 *
 * # Why CodeMirror mounts into a `<div>` React never writes into again
 *
 * The ref is read once, in an effect, to build an `EditorView` that owns
 * everything under it from then on. React reconciling that subtree is
 * exactly the bug this project already met once: the story editor's code
 * block crashed with `RangeError: Selection points outside of document`
 * because it nested a CodeMirror instance *inside* a ProseMirror document, so
 * two independent editors kept two independent ideas of where the caret was,
 * and ProseMirror's own reconciliation could hand CodeMirror a selection that
 * no longer matched what CodeMirror's DOM held. There is no second editor
 * here — this div's children are never React's to reconcile, full stop,
 * which is the same reason an uncontrolled `<textarea>` never hits that class
 * of bug either. See code-editor.test.tsx for what was checked.
 */
export function CodeEditor({
  ariaLabel,
  placeholder: placeholderText,
  getInitialValue,
  onChange,
  errorPosition,
  errorToken,
  className,
}: CodeEditorProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);
  const fallbackRef = useRef<HTMLTextAreaElement>(null);
  const [ready, setReady] = useState(false);

  // Read fresh from wherever it is called, rather than captured once:
  // `onChange` is an inline closure recreated on every render of whatever
  // owns this component (ConsoleEditor's does, same as `onResult` already
  // was before this change). Written from an effect rather than during
  // render itself — the `react-hooks/refs` rule this codebase already leans
  // on elsewhere refuses a ref write in the render body outright, since a
  // render can in principle run without ever committing.
  const onChangeRef = useRef(onChange);
  useEffect(() => {
    onChangeRef.current = onChange;
  });

  // Seeds the fallback field with whatever `getInitialValue` reports — a
  // browser-restored value across a soft reload, most importantly — the
  // instant after mount, before the browser paints. Not read during render:
  // `getInitialValue` reaches into a ref `ConsoleEditor` owns
  // (`mirrorRef.current?.value`), and that ref is not attached to anything
  // until commit, so reading it during *this* component's render would run
  // before the DOM node it targets exists on the very first render — the
  // same class of bug the rest of this file's comments already describe
  // for the real editor's own initial value.
  useLayoutEffect(() => {
    const el = fallbackRef.current;
    const initial = getInitialValue();
    if (el && initial !== "") {
      el.value = initial;
    } else if (el && el.value !== "") {
      // The fallback holds text `getInitialValue` does not know about: it
      // was typed before hydration, into a server-rendered `<textarea>`
      // that accepts input from first paint but whose React `onInput`
      // handler is not listening yet. Nothing reported that text anywhere,
      // so the mirror `ConsoleEditor` submits from is still empty even
      // though this field visibly holds a whole query (finding 2) — report
      // it now, through the exact same path a live edit takes, so the
      // mirror catches up before anything can read it.
      onChangeRef.current(el.value);
    }
    // Runs once, right after the fallback's own first commit — see the
    // comment above for why this cannot be read during render instead.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // What to restore once the host is actually visible — read once, in the
  // mount effect below, before the fallback unmounts out from under the
  // participant. Not applied there directly; see the `ready`-keyed effect
  // just below this one for why.
  const pendingFocusRef = useRef<{ anchor: number; head: number } | null>(null);

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    let live = true;

    // Imported here rather than at the top of the file — see the file doc
    // comment for why this chunk must not be on the play route's critical
    // path.
    void import("./code-editor-core").then((core) => {
      if (!live || !host) return;
      // Read before the swap below can possibly move focus anywhere else.
      const fallback = fallbackRef.current;
      const hadFocus = fallback != null && document.activeElement === fallback;
      const view = core.mountEditor(host, {
        doc: fallback?.value ?? getInitialValue(),
        ariaLabel,
        placeholder: placeholderText,
        onChange: (text) => onChangeRef.current(text),
      });
      viewRef.current = view;
      if (errorPosition != null) core.setErrorPosition(view, errorPosition);
      // A participant typing in the fallback field the instant the chunk
      // finishes loading must not have their cursor dropped on the floor —
      // the fallback is about to unmount out from under them. The fallback's
      // own selection is carried over verbatim (finding 3): forcing the
      // caret to the end would yank it away from a participant who had
      // clicked back into the middle of their query to fix a typo.
      pendingFocusRef.current =
        hadFocus && fallback ? { anchor: fallback.selectionStart, head: fallback.selectionEnd } : null;
      setReady(true);
    });

    return () => {
      live = false;
      viewRef.current?.destroy();
      viewRef.current = null;
    };
    // Built once. ariaLabel/placeholderText are fixed strings from the
    // dictionary for the lifetime of this route (the locale that changes them
    // is a full navigation), and getInitialValue/onChange are read through
    // refs precisely so identity changes in either never rebuild the view.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Restores focus and the caret only once the host has actually stopped
  // being `invisible` (finding 1). `setReady(true)` above is a batched state
  // update — the DOM still carries the `invisible` class (`visibility:
  // hidden`) at the moment that callback runs, and `focus()` inside a
  // `visibility:hidden` subtree is a documented no-op, so calling it there
  // moved focus to nothing and every subsequent keystroke went to `<body>`.
  // A layout effect keyed on `ready` runs after React has committed the DOM
  // without that class, so the host is genuinely visible and focusable by
  // the time this runs.
  useLayoutEffect(() => {
    if (!ready) return;
    const pending = pendingFocusRef.current;
    if (!pending) return;
    pendingFocusRef.current = null;
    const view = viewRef.current;
    if (!view) return;
    const docLength = view.state.doc.length;
    view.focus();
    view.dispatch({
      selection: {
        anchor: Math.min(pending.anchor, docLength),
        head: Math.min(pending.head, docLength),
      },
    });
  }, [ready]);

  useEffect(() => {
    const view = viewRef.current;
    if (!view) return;
    void import("./code-editor-core").then((core) => core.setErrorPosition(view, errorPosition ?? null));
    // errorToken is a dependency on purpose, even though the effect body
    // never reads it — see the prop's own doc comment (finding 4).
  }, [errorPosition, errorToken]);

  return (
    <div className={cn("relative", className)}>
      <div ref={hostRef} className={cn("absolute inset-0", ready ? "" : "invisible")} />
      {!ready && (
        <textarea
          ref={fallbackRef}
          aria-label={ariaLabel}
          placeholder={placeholderText}
          spellCheck={false}
          onInput={(event) => onChangeRef.current(event.currentTarget.value)}
          className="absolute inset-0 h-full w-full resize-none border border-edge bg-sunk p-3 font-mono text-body text-ink outline-none"
        />
      )}
    </div>
  );
}
