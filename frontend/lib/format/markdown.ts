/**
 * The Markdown a WYSIWYG editor produced, with its own artefacts taken out.
 *
 * Milkdown serialises an empty paragraph as a literal `<br />`, and an empty
 * table cell the same way. Raw HTML is deliberately not rendered in this
 * product's Markdown — a story is authored text, not a page — so every one of
 * those arrives on a participant's screen as the four characters, printed.
 *
 * Applied on the way in, so nothing new is stored with them, and on the way
 * out, so the stories already written render correctly without a migration
 * that rewrites somebody's text. One rule, both boundaries, one place to read
 * it.
 *
 * Two things it must never do, which is most of what the tests pin down: a
 * fenced block is content — a story about HTML shows HTML, a game's story
 * shows SQL — and a break an author wrote inside a line is a break they meant.
 * Only a break standing alone as its own paragraph is the editor talking.
 */
export function cleanEditorMarkdown(markdown: string): string {
  const lines = markdown.split("\n");
  const kept: string[] = [];
  let fence: string | null = null;

  for (const line of lines) {
    // Fence tracking first: everything between the markers is content, and
    // the closing marker has to be seen even though the block is untouched.
    const marker = fenceMarker(line);
    if (fence === null && marker !== null) {
      fence = marker;
      kept.push(line);
      continue;
    }
    if (fence !== null) {
      if (marker !== null && marker[0] === fence[0] && marker.length >= fence.length) fence = null;
      kept.push(line);
      continue;
    }

    if (isOnlyBreak(line)) continue;
    kept.push(emptyBreakCells(line));
  }

  // Removing a paragraph leaves the blank line on either side of it, and two
  // of those in a row read as an extra paragraph break the author never
  // wrote. Runs are collapsed back to the one blank line that separates two
  // paragraphs.
  return kept.join("\n").replace(/\n{3,}/g, "\n\n");
}

/** The `<br>` an editor writes for an empty paragraph, alone on its line. */
const ONLY_BREAK = /^\s*<br\s*\/?>\s*$/i;

function isOnlyBreak(line: string): boolean {
  return ONLY_BREAK.test(line);
}

/**
 * A table row's cells that hold nothing but a break, emptied.
 *
 * Only rows, because `|` is what makes a cell: a break between two words of a
 * paragraph is not one, and must be left where the author put it.
 */
function emptyBreakCells(line: string): string {
  if (!line.includes("|")) return line;
  return line.replace(/(?<=\|)(\s*<br\s*\/?>\s*)(?=\|)/gi, "  ");
}

/**
 * The fence a line opens or closes, or null.
 *
 * Returned as the marker itself rather than a boolean, because a fence is
 * closed only by the same character and at least as many of them — which is
 * how a ```` ``` ```` inside a ```` ```` ```` block stays content.
 */
function fenceMarker(line: string): string | null {
  const match = /^\s{0,3}(`{3,}|~{3,})/.exec(line);
  return match ? match[1] : null;
}
