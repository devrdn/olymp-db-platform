import { parserCtx } from "@milkdown/kit/core";
import { Plugin, PluginKey } from "@milkdown/kit/prose/state";
import { Slice } from "@milkdown/kit/prose/model";
import { $prose } from "@milkdown/kit/utils";

/**
 * Pastes the plain-text flavour as Markdown. The `text/html` of a copied web
 * page carries furniture (language labels, "Copy" buttons, line numbers) that
 * would arrive as paragraphs; the document is Markdown, so plain text loses
 * nothing that was text.
 *
 * A paste without plain text (an image, spreadsheet cells) and a paste from
 * this editor fall through to the default handling.
 */
export const pasteAsMarkdown = $prose((ctx) => {
  return new Plugin({
    key: new PluginKey("dbcontest-paste-as-markdown"),
    props: {
      handlePaste: (view, event) => {
        const data = event.clipboardData;
        if (!data) return false;

        // From this editor: ProseMirror's own slice beats a reparse.
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
