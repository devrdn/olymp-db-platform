import { parserCtx } from "@milkdown/kit/core";
import { Plugin, PluginKey } from "@milkdown/kit/prose/state";
import { Slice } from "@milkdown/kit/prose/model";
import { $prose } from "@milkdown/kit/utils";

/**
 * Paste the words, not the page they came from.
 *
 * A rich editor takes `text/html` from the clipboard when a browser offers it,
 * and a browser offers it for anything copied out of a web page. Copying a
 * code sample brings the block's furniture with it — the language label, the
 * "Copy" button's own word, the line numbers down the gutter — because those
 * are real elements sitting inside the region that was selected. They arrive
 * as paragraphs, and somebody then deletes them by hand, line by line.
 *
 * So the plain-text flavour is preferred and parsed as Markdown. That is not a
 * downgrade here: the document *is* Markdown, so text pasted from an editor,
 * a file or a chat keeps its headings, lists and emphasis exactly as it would
 * have. What it loses is the part that was never text.
 *
 * Two things are deliberately left alone. A paste carrying no plain text at
 * all falls through to the ordinary handling, which is what makes pasting an
 * image or a table from a spreadsheet still work. And a paste from inside this
 * editor — the same document, copied and moved — falls through too, so a
 * round trip through the clipboard is not a round trip through Markdown.
 */
export const pasteAsMarkdown = $prose((ctx) => {
  return new Plugin({
    key: new PluginKey("dbcontest-paste-as-markdown"),
    props: {
      handlePaste: (view, event) => {
        const data = event.clipboardData;
        if (!data) return false;

        // Copied from this editor: ProseMirror's own slice is on the
        // clipboard and is a better answer than anything reparsed.
        if (data.types.includes("application/x-pm-slice")) return false;

        const text = data.getData("text/plain");
        if (!text.trim()) return false;

        const parsed = ctx.get(parserCtx)(text);
        if (!parsed) return false;

        view.dispatch(
          view.state.tr
            .replaceSelection(new Slice(parsed.content, 0, 0))
            .scrollIntoView(),
        );
        return true;
      },
    },
  });
});
