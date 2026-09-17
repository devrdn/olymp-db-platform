/**
 * Everything that actually depends on CodeMirror.
 *
 * Split from `code-editor.tsx` so the ~140 KiB (gzipped) it pulls in —
 * `@codemirror/*`, `@lezer/highlight` — is a chunk `import()`ed once the
 * route is already interactive, not part of what the play route ships
 * upfront. `code-editor.tsx` is the only importer, and it always imports this
 * module dynamically; a static `import` of this file from anywhere else
 * would put CodeMirror back on the critical path.
 */
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

/**
 * SQL syntax highlighting — section 11, circle 2's own naming: "CodeMirror 6,
 * our theme". Every colour is a `var()` reference into `styles/tokens.css`,
 * never a literal, for the same reason `bg-[#…]` is an ESLint error in JSX: a
 * colour written here would be a second place the palette lives, and the one
 * this system was built to rule out. A keyword and a comment already have a
 * semantic token (`--accent-ink`, `--ink-3`); a function, a string and a
 * number do not read as any of the eleven the token layer already names, so
 * section 3.1 gives them their own (`--sql-function`, `--sql-string`,
 * `--sql-number`) rather than writing three hex values into this file.
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
 * The editor's own chrome — background, caret, selection, focus — built from
 * the same tokens as everything else in the product, not from CodeMirror's
 * default theme and not from a second palette invented for this component.
 * A new `--sql-editor-…` token is deliberately absent: `--sunk` is already
 * "sunken area (editor)" in section 3.1's own table, so the editor's
 * background is that token directly rather than a new one that would just
 * alias it. `code-editor.tsx`'s fallback textarea uses the same classes this
 * theme's colours are built from, so the handoff between the two is not a
 * visible change of surface.
 *
 * Sizing reads `--font-mono` and `--text-body` — the same face and step
 * section 4 names for "SQL, every number, utility captions" — rather than
 * CodeMirror's own monospace default. Tailwind's `@theme` block emits both as
 * ordinary `:root` custom properties, so referencing them here does not
 * require Tailwind to have touched this file at all.
 */
const editorTheme = EditorView.theme({
  "&": {
    height: "100%",
    backgroundColor: "var(--sunk)",
    color: "var(--ink)",
    // No border. The design's editor is a `--sunk` field between the rules
    // that separate the panes, and nothing else (docs/design/preview.html,
    // `.ed`): "no nested panel inside the window". `--edge` is a solid
    // mid-grey the design spends on exactly one thing, a secondary button's
    // outline, and a box drawn in it around the editor reads as a panel
    // inside a panel.
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
  // The line numbers the design draws down the left of the editor
  // (docs/design/preview.html, "SQL-консоль"). Quiet: the number is a
  // reference, not content, so it takes ink-3 and no fill of its own — a
  // gutter with a background would be a second panel inside a panel, which
  // §3 forbids.
  ".cm-gutters": {
    backgroundColor: "transparent",
    border: "none",
    color: "var(--ink-3)",
    fontVariantNumeric: "tabular-nums",
  },
  ".cm-lineNumbers .cm-gutterElement": { padding: "0 0.5rem 0 0.75rem", minWidth: "2.25rem" },
  // The active line's own number, so a participant reading an error position
  // can find the line without counting.
  ".cm-activeLineGutter": { backgroundColor: "transparent", color: "var(--ink)" },
  ".cm-scroller": { overflow: "auto" },
  ".cm-placeholder": { color: "var(--ink-3)" },
  // `bracketMatching()` below brings its own `EditorView.baseTheme` for
  // these two classes — `#328c8252` / `#bb555544`, CodeMirror's own palette,
  // identical in both themes — which section 3.3 forbids ("no arbitrary
  // colours"). A base theme always loses to a regular one for the same
  // selector regardless of extension order, so repeating the exact
  // selectors here (from `@codemirror/language`'s source) overrides them
  // with tokens this product already has: `--accent-wash` is the same wash
  // `cm-selectionBackground` above already uses for "something is
  // highlighted here", and `--bad-wash` is the one `errorField` uses for
  // "something is wrong" — reused rather than inventing a new pair for what
  // is, structurally, the same two ideas.
  "&.cm-focused .cm-matchingBracket": { backgroundColor: "var(--accent-wash)" },
  "&.cm-focused .cm-nonmatchingBracket": { backgroundColor: "var(--bad-wash)" },
  // The one thing PostgreSQL's own position points at. Not colour alone
  // (WCAG 1.4.1): the wavy underline is a second, shape-based channel, on top
  // of the translated sentence ResultPanel already shows above the editor.
  ".cm-error-position": {
    textDecoration: "underline wavy var(--bad)",
    textDecorationThickness: "2px",
    textUnderlineOffset: "3px",
    backgroundColor: "var(--bad-wash)",
  },
});

/** Sets, or clears, the error mark. `null` clears it. */
const setErrorPositionEffect = StateEffect.define<number | null>();

const errorMark = Decoration.mark({ class: "cm-error-position" });

function markAt(position1Based: number, doc: Text): DecorationSet {
  let from = Math.max(0, position1Based - 1);
  // A position just past the last character — "unexpected end of input" is
  // reported this way — has nothing after it to underline. The last
  // character is still the honest place to point: it is what the participant
  // typed right before PostgreSQL gave up.
  if (from >= doc.length) from = Math.max(0, doc.length - 1);
  const to = Math.min(from + 1, doc.length);
  if (to <= from) return Decoration.none;
  return Decoration.set([errorMark.range(from, to)]);
}

/**
 * Holds the current error decoration.
 *
 * A `StateField` rather than a one-off `Decoration.set` passed at
 * construction: the position arrives after the editor already exists (a
 * query has to be run and refused first), and clears itself on the next
 * keystroke — the text that made PostgreSQL say "here" may no longer be
 * there, and an underline that survives the edit that fixed it would be
 * pointing at nothing.
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

export type { EditorState, EditorView };

/**
 * The extensions each view was built with, so another document can be built
 * with the same ones.
 *
 * A `WeakMap` keyed by the view: the extensions are only ever wanted while
 * that view exists, and a destroyed view takes them with it. Keeping the
 * array rather than rebuilding it per document also keeps the two states
 * genuinely alike — the same keymap, the same update listener, the same
 * theme — which is what lets one be swapped for the other.
 */
const documentExtensions = new WeakMap<EditorView, Extension[]>();

/**
 * Builds the editor and attaches it to `host`, which must already be in the
 * document — `EditorView`'s own `parent` option appends into it immediately.
 */
export function mountEditor(
  host: HTMLElement,
  opts: {
    doc: string;
    ariaLabel: string;
    placeholder: string;
    onChange: (text: string) => void;
    /**
     * Run the query, from ⌘↵ (Ctrl+Enter) inside the editor.
     *
     * Bound here rather than on the surrounding form, and ahead of
     * `defaultKeymap`, because CodeMirror's own default for `Mod-Enter` is
     * `insertBlankLine`: a listener on the form would never see the key, and
     * the participant would get an empty line where the design's own toolbar
     * promises `Выполнить ⌘↵`.
     */
    onSubmit?: () => void;
  },
): EditorView {
  const extensions: Extension[] = [
    history(),
    lineNumbers(),
    // Before defaultKeymap, so this wins Mod-Enter from `insertBlankLine`.
    ...(opts.onSubmit
      ? [
          keymap.of([
            {
              key: "Mod-Enter",
              run: () => {
                opts.onSubmit?.();
                return true;
              },
            },
          ]),
        ]
      : []),
    // `indentWithTab` after the defaults, because it is a fallback rather
    // than an override: Tab keeps its ordinary meaning wherever CodeMirror
    // already has one, and indents otherwise.
    //
    // It does take Tab away from moving focus, which is a real cost for a
    // keyboard user. CodeMirror's own answer is the one kept here: Escape
    // first, then Tab, leaves the editor — and the participant's console is
    // a place people type SQL into for two hours, where a Tab that jumps to
    // the next control is the surprising behaviour.
    keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
    sql({ dialect: PostgreSQL }),
    syntaxHighlighting(highlightStyle),
    bracketMatching(),
    errorField,
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
  return view;
}

/**
 * A second (third, tenth) document for a view that already exists — one SQL
 * tab's own text, with its own undo history and its own caret.
 */
export function newDocument(view: EditorView, doc: string): EditorState {
  return EditorState.create({ doc, extensions: documentExtensions.get(view) ?? [] });
}

/**
 * Shows `next` and hands back the state that was showing, for the caller to
 * keep aside until that document is asked for again.
 *
 * `setState` rather than replacing the whole document with a transaction:
 * a transaction would put the other tab's text into *this* tab's undo
 * history, and one Ctrl+Z would then bring back a query the participant is
 * no longer looking at.
 */
export function swapDocument(view: EditorView, next: EditorState): EditorState {
  const previous = view.state;
  view.setState(next);
  return previous;
}

/**
 * Replaces the showing document's text — a draft recovered from the last
 * visit, which the participant's own editing of that tab is expected to
 * continue from.
 */
export function setDocumentText(view: EditorView, text: string): void {
  view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } });
}

/**
 * Moves, or clears (`position === null`), the mark at a 1-based character
 * offset, and scrolls it into view when setting one — the whole point of
 * carrying the position at all is that the participant sees it without
 * having to go looking.
 */
export function setErrorPosition(view: EditorView, position: number | null): void {
  view.dispatch({ effects: setErrorPositionEffect.of(position) });
  if (position != null) {
    const pos = Math.max(0, Math.min(position - 1, view.state.doc.length));
    view.dispatch({ effects: EditorView.scrollIntoView(pos, { y: "center" }) });
  }
}
