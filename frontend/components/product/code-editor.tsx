"use client";

import { useEffect, useImperativeHandle, useLayoutEffect, useRef, useState } from "react";

import { cn } from "@/lib/utils";

import type { EditorState, EditorView } from "./code-editor-core";

/** The module behind the editor, kept once it has been imported. */
type Core = typeof import("./code-editor-core");

/**
 * What an owner of several documents can ask of the editor from outside
 * React's own data flow — both cases are about a document the editor is
 * holding, which nothing else can reach.
 */
export type CodeEditorHandle = {
  /**
   * Replaces a document's text: the one showing, or one kept aside. A
   * recovered draft is the reason this exists — it arrives after the tab's
   * state was already built from the server's copy.
   */
  setDocumentValue: (id: string, text: string) => void;
  /** Forgets a document for good — a tab that was closed. */
  dropDocument: (id: string) => void;
};

/** One key the screen around the editor claims for itself — see `CodeEditorProps.shortcuts`. */
export type EditorShortcut = { key: string; run: () => void };

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
   * Run whatever this editor is for, from ⌘↵ (Ctrl+Enter) — the shortcut the
   * design's own toolbar prints on its Run button.
   *
   * Passed down to the editor's keymap rather than handled on the surrounding
   * form: CodeMirror's default for `Mod-Enter` is `insertBlankLine`, so a
   * form-level listener would never see the key and the participant would get
   * an empty line instead of an answer. Omitted where there is nothing to run
   * — the game-script editor saves with a button and no shortcut.
   */
  onSubmit?: () => void;
  /**
   * Keys the screen around this editor owns, in CodeMirror's own notation —
   * the workspace's three panel toggles (§8) are what this exists for.
   *
   * They go into the editor's keymap rather than onto a listener somewhere
   * above, because CodeMirror sees a keydown in its own content first and an
   * unclaimed combination is either typed or left to the browser, which reads
   * Ctrl+B in a contenteditable as "bold". A bound one runs the handler,
   * inserts nothing, and is marked handled so nothing above acts on it twice.
   *
   * The keys are read once, when the editor is built; each `run` is read
   * fresh on every press, so an owner that re-renders is never answered by a
   * stale closure.
   */
  shortcuts?: readonly EditorShortcut[];
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
  /**
   * Which document the editor is showing, for an owner that has more than
   * one (the participant's SQL tabs). Changing it puts the current document
   * aside — its text, its undo history and its caret — and shows the named
   * one, without remounting anything and without reporting an edit.
   *
   * Omitted by every owner of a single document, which is the rest of the
   * product: there is then one document and nothing to switch to.
   */
  documentId?: string;
  /**
   * The text of a document the editor has not opened yet. Read once per
   * document, when it is first shown; from then on the editor's own state is
   * the text, until the owner replaces it through `setDocumentValue`.
   */
  getDocumentValue?: (id: string) => string;
  ref?: React.Ref<CodeEditorHandle>;
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
  onSubmit,
  shortcuts,
  documentId,
  getDocumentValue,
  ref,
  className,
}: CodeEditorProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);
  const fallbackRef = useRef<HTMLTextAreaElement>(null);
  const [ready, setReady] = useState(false);

  // The documents that are not showing, and the one that is. Refs rather
  // than state: none of this is rendered, and a swap must not be a render of
  // the tree the editor sits in.
  const coreRef = useRef<Core | null>(null);
  const asideRef = useRef(new Map<string, EditorState>());
  const openIdRef = useRef(documentId);
  const documentIdRef = useRef(documentId);
  const getDocumentValueRef = useRef(getDocumentValue);
  /**
   * Set while this component itself is writing into CodeMirror, so a swap or
   * a recovered draft is not reported back as something the participant
   * typed — which would hand one tab's text to another tab's autosave.
   */
  const writingRef = useRef(false);

  // Read fresh from wherever it is called, rather than captured once:
  // `onChange` is an inline closure recreated on every render of whatever
  // owns this component (ConsoleEditor's does, same as `onResult` already
  // was before this change). Written from an effect rather than during
  // render itself — the `react-hooks/refs` rule this codebase already leans
  // on elsewhere refuses a ref write in the render body outright, since a
  // render can in principle run without ever committing.
  const onChangeRef = useRef(onChange);
  const onSubmitRef = useRef(onSubmit);
  const shortcutsRef = useRef(shortcuts);
  useEffect(() => {
    onChangeRef.current = onChange;
    onSubmitRef.current = onSubmit;
    shortcutsRef.current = shortcuts;
    getDocumentValueRef.current = getDocumentValue;
  });

  /** Reports an edit, unless this component is the one that made it. */
  const report = (text: string) => {
    if (!writingRef.current) onChangeRef.current(text);
  };

  /** Runs `write` with edits attributed to this component rather than the participant. */
  const writing = (write: () => void) => {
    writingRef.current = true;
    try {
      write();
    } finally {
      writingRef.current = false;
    }
  };

  useImperativeHandle(
    ref,
    () => ({
      setDocumentValue: (id, text) => {
        const core = coreRef.current;
        const view = viewRef.current;
        if (id === openIdRef.current) {
          if (core && view) writing(() => core.setDocumentText(view, text));
          else if (fallbackRef.current) fallbackRef.current.value = text;
          return;
        }
        // A document the editor has never opened has no state to replace:
        // it is built from `getDocumentValue` when it is first shown, and
        // the owner has already changed what that reports.
        if (core && view && asideRef.current.has(id)) {
          asideRef.current.set(id, core.newDocument(view, text));
        }
      },
      dropDocument: (id) => {
        asideRef.current.delete(id);
      },
    }),
    // Every path above reads through a ref; nothing here is rebuilt.
    [],
  );

  // Shows the document the owner asks for, keeping the one leaving. Before
  // the real editor exists there is nothing to swap, and the always-typable
  // fallback field is what has to carry the text instead.
  useLayoutEffect(() => {
    documentIdRef.current = documentId;
    if (documentId === undefined || openIdRef.current === documentId) return;
    const core = coreRef.current;
    const view = viewRef.current;
    const text = getDocumentValueRef.current?.(documentId) ?? "";
    if (!core || !view) {
      if (fallbackRef.current) fallbackRef.current.value = text;
      openIdRef.current = documentId;
      return;
    }
    const leaving = openIdRef.current;
    const next = asideRef.current.get(documentId) ?? core.newDocument(view, text);
    asideRef.current.delete(documentId);
    writing(() => {
      const previous = core.swapDocument(view, next);
      if (leaving !== undefined) asideRef.current.set(leaving, previous);
    });
    openIdRef.current = documentId;
  }, [documentId]);

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
      coreRef.current = core;
      // Whichever document the owner is pointing at by now: the switch may
      // have happened while this chunk was still on its way, and the
      // fallback field has been carrying that document's text since.
      openIdRef.current = documentIdRef.current;
      const view = core.mountEditor(host, {
        doc: fallback?.value ?? getInitialValue(),
        ariaLabel,
        placeholder: placeholderText,
        onChange: report,
        // Read fresh through the ref for the same reason `onChange` is: the
        // editor is built once and the callback is an inline closure that is
        // recreated on every render of whatever owns this component.
        onSubmit: onSubmit ? () => onSubmitRef.current?.() : undefined,
        // The keys as they are at mount; the handler behind each one is
        // looked up by that key on every press, for the reason above.
        shortcuts: shortcuts?.map(({ key }) => ({
          key,
          run: () => {
            const claimed = shortcutsRef.current?.find((shortcut) => shortcut.key === key);
            // Not claimed any more: hand the key back rather than swallow it.
            if (!claimed) return false;
            claimed.run();
            return true;
          },
        })),
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

    const aside = asideRef.current;
    return () => {
      live = false;
      viewRef.current?.destroy();
      viewRef.current = null;
      coreRef.current = null;
      aside.clear();
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
          onInput={(event) => report(event.currentTarget.value)}
          className="absolute inset-0 h-full w-full resize-none border border-edge bg-sunk p-3 font-mono text-body text-ink outline-none"
        />
      )}
    </div>
  );
}
