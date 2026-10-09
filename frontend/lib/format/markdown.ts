/**
 * Removes the `<br />` Milkdown writes for an empty paragraph or table cell,
 * which would otherwise print literally (raw HTML is not rendered). Applied
 * both when saving and when rendering, so older stories need no migration.
 * Fenced blocks and a break inside a line are the author's and kept.
 */
export function cleanEditorMarkdown(markdown: string): string {
  const lines = markdown.split("\n");
  const kept: string[] = [];
  let fence: string | null = null;

  for (const line of lines) {
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

  // A removed paragraph leaves its blank lines on both sides; collapse them.
  return kept.join("\n").replace(/\n{3,}/g, "\n\n");
}

const ONLY_BREAK = /^\s*<br\s*\/?>\s*$/i;

function isOnlyBreak(line: string): boolean {
  return ONLY_BREAK.test(line);
}

/** Empties table cells holding only a break; a break inside prose is left alone. */
function emptyBreakCells(line: string): string {
  if (!line.includes("|")) return line;
  return line.replace(/(?<=\|)(\s*<br\s*\/?>\s*)(?=\|)/gi, "  ");
}

/**
 * The fence marker a line opens or closes, or null. A fence closes only with
 * the same character, at least as many times.
 */
function fenceMarker(line: string): string | null {
  const match = /^\s{0,3}(`{3,}|~{3,})/.exec(line);
  return match ? match[1] : null;
}
