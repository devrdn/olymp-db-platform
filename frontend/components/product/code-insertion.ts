/**
 * Where inserted text goes, apart from CodeMirror so the fallback field uses
 * the same rule before the editor has loaded.
 */

/** The caret's line: its text and the offset where it ends. */
export type CaretLine = { text: string; to: number };

/**
 * At the caret when its line is blank; otherwise on a new line after the
 * caret's line, so the inserted text never splits what is already there.
 */
export function placeInsertion(line: CaretLine, caret: number, text: string): { at: number; insert: string } {
  return line.text.trim() === "" ? { at: caret, insert: text } : { at: line.to, insert: `\n${text}` };
}
