"use client";

import { useEffect, useImperativeHandle, useLayoutEffect, useRef, useState } from "react";

import { cn } from "@/lib/utils";

import type { EditorState, EditorView } from "./code-editor-core";

type Core = typeof import("./code-editor-core");

/** Operations on documents the editor holds, which nothing outside it can reach. */
export type CodeEditorHandle = {
  /**
   * Replaces a document's text, showing or kept aside. Exists for a recovered
   * draft, which arrives after the tab was built from the server's copy.
   */
  setDocumentValue: (id: string, text: string) => void;
  /**
   * Forgets a closed tab's document. If it is the one showing, the owner then
   * points the editor elsewhere and the state left behind is discarded.
   */
  dropDocument: (id: string) => void;
};

export type EditorShortcut = { key: string; run: () => void };

export type CodeEditorProps = {
  ariaLabel: string;
  placeholder: string;
  /**
   * Read once for the fallback field and once when CodeMirror is built; not a
   * controlled value, since CodeMirror owns the document from then on.
   */
  getInitialValue: () => string;
  /**
   * Called with the current text on every change, fallback keystrokes included.
   * Must not re-render the owner: write to a ref or an uncontrolled DOM node.
   * It is always called from outside React's render cycle.
   */
  onChange: (text: string) => void;
  /**
   * Bound to ⌘↵ / Ctrl+Enter in the editor's keymap. A form-level listener
   * would never see the key: CodeMirror's default for `Mod-Enter` inserts a
   * blank line.
   */
  onSubmit?: () => void;
  /**
   * Keys the surrounding screen owns, in CodeMirror notation. They go into the
   * editor's keymap because CodeMirror sees a keydown first, and an unclaimed
   * one is typed or left to the browser (Ctrl+B means "bold" in a
   * contenteditable). The keys are read once at build; each `run` is read fresh
   * on every press.
   */
  shortcuts?: readonly EditorShortcut[];
  /**
   * 1-based character offset (PostgreSQL's convention) to underline, or
   * `undefined` for none. Ignored while the fallback field shows; applied once
   * CodeMirror mounts.
   */
  errorPosition?: number;
  /**
   * Must be a new reference per settled run (e.g. the action state). A second
   * error at the same offset is the same number, so without this the underline,
   * which CodeMirror clears on every edit, would not come back. Its value is
   * never read.
   */
  errorToken?: unknown;
  /**
   * The document showing, for an owner with several (the participant's SQL
   * tabs). Changing it keeps the current document's text, undo history and
   * caret aside and shows the named one, without remounting or reporting an
   * edit. Omitted by single-document owners.
   */
  documentId?: string;
  /**
   * Initial text of a document, read once when it is first shown; after that
   * only `setDocumentValue` replaces it.
   */
  getDocumentValue?: (id: string) => string;
  ref?: React.Ref<CodeEditorHandle>;
  className?: string;
};

/**
 * SQL editor: CodeMirror 6 in the product theme, underlining the position of
 * a PostgreSQL syntax error.
 *
 * CodeMirror (`code-editor-core.ts`, about 140 KiB gzipped) is loaded with a
 * dynamic `import()` to keep it off the play route's critical path. Until it
 * arrives, a server-rendered `<textarea>` stands in so keystrokes typed
 * before hydration are not lost; its text and selection carry over when the
 * real editor mounts.
 *
 * CodeMirror mounts into a `<div>` React never reconciles again, so the two
 * never disagree about the DOM or the selection.
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

  // Refs, not state: a document swap must not re-render the owner's tree.
  const coreRef = useRef<Core | null>(null);
  const asideRef = useRef(new Map<string, EditorState>());
  const openIdRef = useRef(documentId);
  const documentIdRef = useRef(documentId);
  /** A showing document its owner dropped: the next swap discards it. */
  const droppedRef = useRef<string | undefined>(undefined);
  const getDocumentValueRef = useRef(getDocumentValue);
  /**
   * Set while this component writes into CodeMirror, so a swap or recovered
   * draft is not reported as typing (which would hand one tab's text to another
   * tab's autosave).
   */
  const writingRef = useRef(false);

  // Callbacks are read through refs because the editor is built once and owners
  // pass inline closures. Refs are written in an effect, not during render
  // (`react-hooks/refs`).
  const onChangeRef = useRef(onChange);
  const onSubmitRef = useRef(onSubmit);
  const shortcutsRef = useRef(shortcuts);
  useEffect(() => {
    onChangeRef.current = onChange;
    onSubmitRef.current = onSubmit;
    shortcutsRef.current = shortcuts;
    getDocumentValueRef.current = getDocumentValue;
  });

  const report = (text: string) => {
    if (!writingRef.current) onChangeRef.current(text);
  };

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
        // A never-opened document has no state yet; it is built from
        // `getDocumentValue` when first shown.
        if (core && view && asideRef.current.has(id)) {
          asideRef.current.set(id, core.newDocument(view, text));
        }
      },
      dropDocument: (id) => {
        asideRef.current.delete(id);
        // The showing document lives in the view, not the map. Mark it so the
        // owner's next swap discards it instead of storing it forever; the view
        // must keep showing it until then.
        if (id === openIdRef.current) droppedRef.current = id;
      },
    }),
    [],
  );

  // Before CodeMirror exists, the fallback field carries the document's text.
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
      if (leaving !== undefined && leaving !== droppedRef.current) {
        asideRef.current.set(leaving, previous);
      }
    });
    if (leaving === droppedRef.current) droppedRef.current = undefined;
    openIdRef.current = documentId;
  }, [documentId]);

  // Seeds the fallback before paint. Not done during render: `getInitialValue`
  // may read an owner's ref that is attached only at commit.
  useLayoutEffect(() => {
    const el = fallbackRef.current;
    const initial = getInitialValue();
    if (el && initial !== "") {
      el.value = initial;
    } else if (el && el.value !== "") {
      // Typed before hydration, when no `onInput` was listening: report it now
      // so the owner's copy is not left empty.
      onChangeRef.current(el.value);
    }
    // Runs once, after the fallback's first commit.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Fallback focus and selection, captured at mount and applied once the host
  // is visible.
  const pendingFocusRef = useRef<{ anchor: number; head: number } | null>(null);

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    let live = true;

    void import("./code-editor-core").then((core) => {
      if (!live || !host) return;
      // Read before the swap can move focus.
      const fallback = fallbackRef.current;
      const hadFocus = fallback != null && document.activeElement === fallback;
      coreRef.current = core;
      // The owner may have switched documents while the chunk was loading.
      openIdRef.current = documentIdRef.current;
      const view = core.mountEditor(host, {
        doc: fallback?.value ?? getInitialValue(),
        ariaLabel,
        placeholder: placeholderText,
        onChange: report,
        onSubmit: onSubmit ? () => onSubmitRef.current?.() : undefined,
        shortcuts: shortcuts?.map(({ key }) => ({
          key,
          run: () => {
            const claimed = shortcutsRef.current?.find((shortcut) => shortcut.key === key);
            // No longer claimed: let the key through.
            if (!claimed) return false;
            claimed.run();
            return true;
          },
        })),
      });
      viewRef.current = view;
      if (errorPosition != null) core.setErrorPosition(view, errorPosition);
      // Keep the participant's caret and selection: the fallback is about to
      // unmount.
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
    // Built once: the labels change only with the locale, which is a full
    // navigation, and callbacks are read through refs.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Focus only after `ready` commits: `focus()` inside a `visibility: hidden`
  // subtree is a no-op, and keystrokes would go to `<body>`.
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
    // errorToken re-fires the effect for a repeated position.
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
