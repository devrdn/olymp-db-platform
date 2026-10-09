import Markdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";

import { cleanEditorMarkdown } from "@/lib/format/markdown";

import { cn } from "@/lib/utils";

/**
 * Authored Markdown, rendered. One renderer for the constructor's preview and
 * the participant's screen, so an author sees what participants see.
 *
 * A security boundary: a contest manager writes the story and every participant
 * reads it. It produces React elements, never an HTML string, so there is no
 * `dangerouslySetInnerHTML` and no sanitiser; raw HTML is not parsed and shows
 * as text. GitHub-flavoured Markdown adds tables and strikethrough.
 */

/** Protocols a link may use; anything else can run code. */
const SAFE = new Set(["http:", "https:", "mailto:"]);

/**
 * `react-markdown` already refuses dangerous protocols; this keeps the rule in
 * the repository rather than in a dependency's default.
 */
function safeUrl(url: string): string {
  try {
    return SAFE.has(new URL(url, "https://placeholder.invalid").protocol) ? url : "";
  } catch {
    return "";
  }
}

const components: Components = {
  // Opens in a new tab so a participant under a timer keeps the page;
  // `noopener` cuts `window.opener`.
  a: ({ href, children, ...rest }) => (
    <a {...rest} href={href} target="_blank" rel="noopener noreferrer">
      {children}
    </a>
  ),
};

/**
 * The story's typography, held here so the preview and the participant's screen
 * cannot drift; `className` is for layout only.
 *
 * Under `print:` a heading stays with its paragraph, a paragraph is never a
 * lone orphan or widow line, and a code block never splits across pages.
 */
const PROSE = [
  "font-serif text-narrative text-ink",
  "[&>*+*]:mt-4",
  "[&_h1]:font-sans [&_h1]:text-h2 [&_h1]:text-ink [&_h1]:mt-8 print:[&_h1]:break-after-avoid",
  "[&_h2]:font-sans [&_h2]:text-h3 [&_h2]:text-ink [&_h2]:mt-7 print:[&_h2]:break-after-avoid",
  "[&_h3]:font-sans [&_h3]:text-row [&_h3]:text-ink [&_h3]:mt-6 print:[&_h3]:break-after-avoid",
  "print:[&_p]:[orphans:3] print:[&_p]:[widows:3]",
  "print:[&_pre]:break-inside-avoid",
  "[&_strong]:font-semibold [&_em]:italic",
  "[&_ul]:list-disc [&_ol]:list-decimal [&_ul]:pl-5 [&_ol]:pl-5 [&_li]:mt-1.5",
  "[&_a]:text-accent [&_a]:underline [&_a]:underline-offset-4",
  "[&_blockquote]:border-l-2 [&_blockquote]:border-line-2 [&_blockquote]:pl-4 [&_blockquote]:text-ink-2",
  "[&_code]:font-mono [&_code]:text-data [&_code]:bg-sunk [&_code]:px-1 [&_code]:py-0.5",
  "[&_pre]:bg-sunk [&_pre]:p-3 [&_pre]:overflow-x-auto [&_pre_code]:bg-transparent [&_pre_code]:p-0",
  "[&_table]:w-full [&_table]:border-collapse [&_table]:text-body [&_table]:font-sans",
  "[&_th]:border-b [&_th]:border-line-2 [&_th]:py-1.5 [&_th]:text-left [&_th]:font-mono [&_th]:text-label [&_th]:uppercase [&_th]:text-ink-3",
  "[&_td]:border-b [&_td]:border-line [&_td]:py-1.5 [&_td]:pr-4 [&_td]:align-baseline",
  "[&_hr]:border-line",
].join(" ");

export function StoryText({ markdown, className }: { markdown: string; className?: string }) {
  // Cleaned on output too: older stories still carry the editor's `<br />`
  // artefacts, which would otherwise print as text. Doing it here avoids
  // rewriting stored text.
  const text = cleanEditorMarkdown(markdown);
  if (text.trim() === "") return null;

  return (
    <div className={cn(PROSE, className)}>
      <Markdown remarkPlugins={[remarkGfm]} urlTransform={safeUrl} components={components}>
        {text}
      </Markdown>
    </div>
  );
}
