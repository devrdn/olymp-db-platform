/**
 * What the SQL editor offers as the participant types: PostgreSQL's keywords
 * (from `sql()` in `code-editor-core.ts`) and common functions, and the
 * contest's tables and columns from the schema document. Imported only by
 * `code-editor-core.ts`, so it loads with CodeMirror and never before.
 */
import {
  autocompletion,
  closeCompletion,
  completeFromList,
  completionStatus,
  ifNotIn,
  selectedCompletion,
  type Completion,
  type CompletionContext,
  type CompletionResult,
  type CompletionSource,
} from "@codemirror/autocomplete";
import { PostgreSQL, schemaCompletionSource, type SQLNamespace } from "@codemirror/lang-sql";
import { syntaxTree } from "@codemirror/language";
import { Prec, type Extension } from "@codemirror/state";
import { keymap, type EditorView } from "@codemirror/view";

import type { GameSchema } from "@/lib/api/schema";
import { quoteIdentifier } from "@/lib/sql/identifier";

/** Nodes inside which nothing is offered, as `sql()` does for keywords. */
const NOT_CODE = ["QuotedIdentifier", "String", "LineComment", "BlockComment", "."];

/** The same, less quoted identifiers: a column's name may be typed after its opening quote. */
const NOT_NAME = ["String", "LineComment", "BlockComment", "."];

/**
 * Built-in functions a contest query reaches for that the dialect's word list
 * lacks (it already carries `count`, `sum`, `coalesce`, `lower` and the like).
 * Upper case, like the keywords.
 */
const FUNCTIONS = [
  "age", "array_length", "bool_and", "bool_or", "concat", "concat_ws", "date_part", "date_trunc",
  "format", "generate_series", "initcap", "json_agg", "json_build_object", "jsonb_agg",
  "jsonb_build_object", "lpad", "make_date", "md5", "now", "pi", "random", "regexp_matches",
  "regexp_replace", "round", "rpad", "sign", "split_part", "stddev", "string_agg", "strpos",
  "to_char", "to_date", "to_number", "to_timestamp", "trunc", "unnest", "variance",
].map((name): Completion => ({ label: name.toUpperCase(), type: "function", boost: -1 }));

/** A name as the editor writes it when chosen: quoted when PostgreSQL would otherwise misread it. */
function nameOption(name: string, type: string, detail?: string): Completion {
  const written = quoteIdentifier(name);
  return { label: name, type, detail, apply: written === name ? undefined : written };
}

function columnOptions(table: GameSchema["tables"][number]): Completion[] {
  return table.columns.map((column) => nameOption(column.name, "property", column.type));
}

/**
 * The schema in lang-sql's shape. A dot in a key separates a schema from a
 * table there, so a dot inside a table's own name is escaped.
 */
function namespaceOf(schema: GameSchema): SQLNamespace {
  const namespace: Record<string, SQLNamespace> = {};
  for (const table of schema.tables) {
    namespace[table.name.replace(/\./g, "\\.")] = {
      self: nameOption(table.name, "type"),
      children: columnOptions(table),
    };
  }
  return namespace;
}

/** The name an identifier node refers to: folded to lower case unless quoted, as PostgreSQL reads it. */
function referencedName(text: string, quoted: boolean): string {
  return quoted ? text.slice(1, -1).replace(/""/g, '"') : text.toLowerCase();
}

/**
 * Columns without a table name in front: those of every table the statement
 * names anywhere, subqueries included. lang-sql completes only `table.column`.
 */
function columnsInScope(schema: GameSchema): CompletionSource {
  const byTable = new Map(schema.tables.map((table) => [table.name, columnOptions(table)]));
  return (context: CompletionContext): CompletionResult | null => {
    const { state, pos } = context;
    const at = syntaxTree(state).resolveInner(pos, -1);
    // Inside a quoted name the word runs from its opening quote, and a closing
    // quote already after the caret is replaced rather than doubled.
    const quoted = at.name === "QuotedIdentifier";
    if (quoted && pos === at.to && at.to - at.from > 1 && state.sliceDoc(at.to - 1, at.to) === '"') return null;
    const word = quoted ? { from: at.from, to: pos } : context.matchBefore(/\w*$/);
    if (!word || (word.from === word.to && !context.explicit)) return null;
    // After `table.` lang-sql offers that table's columns alone.
    if (state.sliceDoc(word.from - 1, word.from) === ".") return null;
    const to = quoted && state.sliceDoc(pos, pos + 1) === '"' ? pos + 1 : undefined;
    let statement = at;
    while (statement.parent && statement.name !== "Statement") statement = statement.parent;
    if (statement.name !== "Statement") return null;

    const options = new Map<string, Completion>();
    syntaxTree(state).iterate({
      from: statement.from,
      to: statement.to,
      enter: (node) => {
        if (node.name !== "Identifier" && node.name !== "QuotedIdentifier") return;
        // The word being typed is not yet a reference to anything.
        if (node.from < pos && pos <= node.to) return;
        const text = state.sliceDoc(node.from, node.to);
        const columns = byTable.get(referencedName(text, node.name === "QuotedIdentifier"));
        for (const column of columns ?? []) {
          if (!options.has(column.label)) options.set(column.label, column);
        }
      },
    });
    if (options.size === 0) return null;
    if (!quoted) return { from: word.from, options: [...options.values()], validFor: /^\w*$/ };
    const inQuotes = [...options.values()].map((option) => {
      const written = `"${option.label.replace(/"/g, '""')}"`;
      return { ...option, label: written, apply: written };
    });
    return { from: word.from, to, options: inQuotes, validFor: /^"\w*"?$/ };
  };
}

const offerNothing: CompletionSource = () => null;

/**
 * How long after the popup opens a key that accepts is ignored, so a newline
 * typed just as it appears is not taken for a choice (CodeMirror's default).
 */
export const INTERACTION_DELAY_MS = 75;

/**
 * Enter on a word already typed whole closes the popup and breaks the line:
 * the selected option's name or written form equals the word, ignoring case.
 * Otherwise every line ending in a table name or a keyword would need Enter
 * twice, and a keyword such as `end` would be rewritten as a quoted column
 * named after it. Tab still takes the option's written form. Returns false
 * either way, so the completion keymap or the line break runs next.
 */
function breakLineOnTypedWord(view: EditorView): boolean {
  if (completionStatus(view.state) !== "active") return false;
  const option = selectedCompletion(view.state);
  if (!option) return false;
  const head = view.state.selection.main.head;
  const line = view.state.doc.lineAt(head);
  const typed = (/[\w$]*$/.exec(line.text.slice(0, head - line.from))?.[0] ?? "").toLowerCase();
  if (typed === "") return false;
  const forms = [option.label, typeof option.apply === "string" ? option.apply : option.label];
  if (forms.some((form) => form.toLowerCase() === typed)) closeCompletion(view);
  return false;
}

/**
 * Completion for one editor view. The schema is held here rather than in each
 * document's configuration because the view holds several documents (the SQL
 * tabs) and the schema arrives, or changes after a reset, for all of them at
 * once; the sources read it on every request.
 */
export function sqlCompletion(): {
  extension: Extension;
  setSchema: (schema: GameSchema | null) => void;
} {
  let tables = offerNothing;
  let columns = offerNothing;
  return {
    extension: [
      // Ahead of the completion keymap, which has the same precedence and comes later.
      Prec.highest(keymap.of([{ key: "Enter", run: breakLineOnTypedWord }])),
      // Icons off: CodeMirror's are glyphs in no design system; a column shows
      // its type instead.
      autocompletion({ icons: false, interactionDelay: INTERACTION_DELAY_MS }),
      PostgreSQL.language.data.of({ autocomplete: ifNotIn(NOT_CODE, completeFromList(FUNCTIONS)) }),
      PostgreSQL.language.data.of({ autocomplete: (context: CompletionContext) => tables(context) }),
      PostgreSQL.language.data.of({ autocomplete: (context: CompletionContext) => columns(context) }),
    ],
    setSchema: (schema) => {
      tables = schema
        ? schemaCompletionSource({ dialect: PostgreSQL, schema: namespaceOf(schema) })
        : offerNothing;
      columns = schema ? ifNotIn(NOT_NAME, columnsInScope(schema)) : offerNothing;
    },
  };
}
