/**
 * Everything that depends on CodeMirror (~140 KiB gzipped). Only
 * `code-editor.tsx` imports it, and only dynamically; a static import anywhere
 * would put CodeMirror on the play route's critical path.
 */
import { acceptCompletion, closeCompletion } from "@codemirror/autocomplete";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { PostgreSQL, sql } from "@codemirror/lang-sql";
import { HighlightStyle, bracketMatching, syntaxHighlighting } from "@codemirror/language";
import { EditorState, StateEffect, StateField, type Extension, type Text } from "@codemirror/state";
import {
  Decoration,
  type DecorationSet,
  EditorView,
  keymap,
  lineNumbers,
  placeholder,
} from "@codemirror/view";
import { tags } from "@lezer/highlight";

import type { GameSchema } from "@/lib/api/schema";

import { placeInsertion } from "./code-insertion";
import { sqlCompletion } from "./sql-completion";

/**
 * SQL highlighting. Colours are `var()` references into `styles/tokens.css`,
 * never literals, so the palette lives in one place.
 */
const highlightStyle = HighlightStyle.define([
  { tag: tags.keyword, color: "var(--accent-ink)" },
  { tag: [tags.function(tags.variableName), tags.function(tags.propertyName)], color: "var(--sql-function)" },
  { tag: tags.string, color: "var(--sql-string)" },
  { tag: tags.number, color: "var(--sql-number)" },
  { tag: tags.comment, color: "var(--ink-3)", fontStyle: "italic" },
  { tag: [tags.operator, tags.punctuation], color: "var(--ink-2)" },
]);

/**
 * Editor chrome from the product tokens rather than CodeMirror's default theme.
 * The fallback textarea in `code-editor.tsx` uses the same tokens, so the
 * handoff is not a visible change of surface.
 */
const editorTheme = EditorView.theme({
  "&": {
    height: "100%",
    backgroundColor: "var(--sunk)",
    color: "var(--ink)",
    // No border: a box around the editor reads as a panel inside a panel.
    fontFamily: "var(--font-mono)",
    fontSize: "var(--text-body)",
  },
  "&.cm-focused": {
    outline: "2px solid var(--accent-ink)",
    outlineOffset: "1px",
  },
  ".cm-content": {
    caretColor: "var(--accent-ink)",
    lineHeight: "var(--text-body--line-height)",
    fontVariantNumeric: "tabular-nums",
    padding: "0.75rem",
  },
  ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--accent-ink)" },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection": {
    backgroundColor: "var(--accent-wash)",
  },
  // Gutter without a fill, so it does not read as a second panel.
  ".cm-gutters": {
    backgroundColor: "transparent",
    border: "none",
    color: "var(--ink-3)",
    fontVariantNumeric: "tabular-nums",
  },
  ".cm-lineNumbers .cm-gutterElement": { padding: "0 0.5rem 0 0.75rem", minWidth: "2.25rem" },
  ".cm-activeLineGutter": { backgroundColor: "transparent", color: "var(--ink)" },
  ".cm-scroller": { overflow: "auto" },
  ".cm-placeholder": { color: "var(--ink-3)" },
  // `bracketMatching()` ships a base theme with literal colours. A base theme
  // always loses to a regular one for the same selector, so repeating its
  // selectors here swaps in product tokens.
  "&.cm-focused .cm-matchingBracket": { backgroundColor: "var(--accent-wash)" },
  "&.cm-focused .cm-nonmatchingBracket": { backgroundColor: "var(--bad-wash)" },
  // The wavy underline is a shape cue, so the error is not shown by colour
  // alone (WCAG 1.4.1).
  ".cm-error-position": {
    textDecoration: "underline wavy var(--bad)",
    textDecorationThickness: "2px",
    textUnderlineOffset: "3px",
    backgroundColor: "var(--bad-wash)",
  },
  // The completion popup, drawn like the product's other popups. These repeat
  // the base theme's selectors so the product tokens win over its literals.
  ".cm-tooltip": {
    backgroundColor: "var(--panel)",
    border: "1px solid var(--line-2)",
    color: "var(--ink)",
  },
  ".cm-tooltip.cm-tooltip-autocomplete > ul": {
    fontFamily: "var(--font-mono)",
    fontSize: "var(--text-body)",
    maxHeight: "14em",
    padding: "0.25rem 0",
  },
  ".cm-tooltip.cm-tooltip-autocomplete > ul > li": {
    padding: "0.125rem 0.75rem",
    lineHeight: "var(--text-body--line-height)",
  },
  ".cm-tooltip-autocomplete ul li[aria-selected]": {
    background: "var(--select-bg)",
    color: "var(--select-fg)",
  },
  ".cm-completionMatchedText": { textDecoration: "none", fontWeight: "600" },
  ".cm-completionDetail": { color: "var(--ink-3)", fontStyle: "normal", marginLeft: "0.75em" },
});

const setErrorPositionEffect = StateEffect.define<number | null>();

const errorMark = Decoration.mark({ class: "cm-error-position" });

function markAt(position1Based: number, doc: Text): DecorationSet {
  let from = Math.max(0, position1Based - 1);
  // "Unexpected end of input" points past the last character; underline the
  // last one instead.
  if (from >= doc.length) from = Math.max(0, doc.length - 1);
  const to = Math.min(from + 1, doc.length);
  if (to <= from) return Decoration.none;
  return Decoration.set([errorMark.range(from, to)]);
}

/**
 * The current error decoration. It clears on the next edit, since the text
 * PostgreSQL pointed at may no longer be there.
 */
const errorField = StateField.define<DecorationSet>({
  create: () => Decoration.none,
  update(marks, tr) {
    if (tr.docChanged) marks = Decoration.none;
    for (const effect of tr.effects) {
      if (effect.is(setErrorPositionEffect)) {
        marks = effect.value == null ? Decoration.none : markAt(effect.value, tr.state.doc);
      }
    }
    return marks;
  },
  provide: (field) => EditorView.decorations.from(field),
});

/**
 * Whether the participant has placed the caret in this document: clicked,
 * moved it or edited. A document's state carries it, so each SQL tab has its
 * own. Until then the caret sits at the start only because nobody moved it.
 */
const caretPlacedField = StateField.define<boolean>({
  create: () => false,
  update: (placed, tr) =>
    placed || ["select", "input", "delete", "move", "undo", "redo"].some((event) => tr.isUserEvent(event)),
});

export type { EditorState, EditorView };

/**
 * Each view's extensions, so another document gets exactly the same keymap,
 * listener and theme and can be swapped in.
 */
const documentExtensions = new WeakMap<EditorView, Extension[]>();

/** Each view's way to change the tables and columns it offers. */
const completionSchemas = new WeakMap<EditorView, (schema: GameSchema | null) => void>();

/** Builds the editor inside `host`, which must already be in the document. */
export function mountEditor(
  host: HTMLElement,
  opts: {
    doc: string;
    ariaLabel: string;
    placeholder: string;
    onChange: (text: string) => void;
    /**
     * Bound to ⌘↵ / Ctrl+Enter ahead of `defaultKeymap`, whose `Mod-Enter`
     * inserts a blank line.
     */
    onSubmit?: () => void;
    /**
     * Keys the surrounding screen owns, in CodeMirror notation (`Mod-b`). A
     * command that returns true makes CodeMirror call `preventDefault`, so the
     * browser does not act on the key (Ctrl+B is "bold" in a contenteditable).
     * Read once; each `run` must look up the owner's current handler and return
     * `false` when the key is no longer claimed.
     */
    shortcuts?: readonly { key: string; run: () => boolean }[];
  },
): EditorView {
  const completion = sqlCompletion();
  const extensions: Extension[] = [
    history(),
    lineNumbers(),
    // Before defaultKeymap, so this wins Mod-Enter from `insertBlankLine`.
    ...(opts.onSubmit
      ? [
          keymap.of([
            {
              key: "Mod-Enter",
              run: (view) => {
                closeCompletion(view);
                opts.onSubmit?.();
                return true;
              },
            },
          ]),
        ]
      : []),
    // Also before the defaults, so the screen's keys win. `Mod` is ⌘ on a Mac,
    // so `Ctrl-b` keeps its editor meaning there.
    ...(opts.shortcuts?.length ? [keymap.of(opts.shortcuts.map(({ key, run }) => ({ key, run })))] : []),
    // `indentWithTab` comes after the defaults, so it only applies where Tab
    // has no other meaning. It takes Tab away from focus movement; Escape then
    // Tab still leaves the editor. Ahead of both, Tab accepts an open
    // completion; `acceptCompletion` declines when none is open.
    keymap.of([{ key: "Tab", run: acceptCompletion }, ...defaultKeymap, ...historyKeymap, indentWithTab]),
    // Upper-case keywords, like the SQL the product writes itself.
    sql({ dialect: PostgreSQL, upperCaseKeywords: true }),
    completion.extension,
    syntaxHighlighting(highlightStyle),
    bracketMatching(),
    errorField,
    caretPlacedField,
    editorTheme,
    EditorView.lineWrapping,
    EditorView.contentAttributes.of({ "aria-label": opts.ariaLabel, spellcheck: "false" }),
    placeholder(opts.placeholder),
    EditorView.updateListener.of((update) => {
      if (update.docChanged) opts.onChange(update.state.doc.toString());
    }),
  ];

  const view = new EditorView({
    state: EditorState.create({ doc: opts.doc, extensions }),
    parent: host,
  });
  documentExtensions.set(view, extensions);
  completionSchemas.set(view, completion.setSchema);
  return view;
}

/**
 * Offers the tables and columns of `schema` in every document of the view,
 * or only keywords on `null` (a contest that hides its schema).
 */
export function setCompletionSchema(view: EditorView, schema: GameSchema | null): void {
  completionSchemas.get(view)?.(schema);
}

/** Another document for an existing view, with its own undo history and caret. */
export function newDocument(view: EditorView, doc: string): EditorState {
  return EditorState.create({ doc, extensions: documentExtensions.get(view) ?? [] });
}

/**
 * Shows `next` and returns the state that was showing. Uses `setState`, not a
 * transaction, so one tab's text never enters another tab's undo history.
 */
export function swapDocument(view: EditorView, next: EditorState): EditorState {
  const previous = view.state;
  view.setState(next);
  return previous;
}

/** Replaces the showing document's text with a recovered draft. */
export function setDocumentText(view: EditorView, text: string): void {
  view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } });
}

/**
 * Inserts text into the showing document as one edit (undoable, and reported
 * like typing), puts the caret after it and gives the editor the focus. In a
 * document whose caret nobody placed it goes at the end, so it never splits
 * the statement the document opens with.
 */
export function insertAtCursor(view: EditorView, text: string): void {
  const caret = view.state.field(caretPlacedField, false) ? view.state.selection.main.head : view.state.doc.length;
  const { at, insert } = placeInsertion(view.state.doc.lineAt(caret), caret, text);
  view.dispatch({
    changes: { from: at, insert },
    selection: { anchor: at + insert.length },
    scrollIntoView: true,
    userEvent: "input.paste",
  });
  view.focus();
}

/**
 * Moves the mark to a 1-based character offset and scrolls it into view, or
 * clears it on `null`.
 */
export function setErrorPosition(view: EditorView, position: number | null): void {
  view.dispatch({ effects: setErrorPositionEffect.of(position) });
  if (position != null) {
    const pos = Math.max(0, Math.min(position - 1, view.state.doc.length));
    view.dispatch({ effects: EditorView.scrollIntoView(pos, { y: "center" }) });
  }
}
