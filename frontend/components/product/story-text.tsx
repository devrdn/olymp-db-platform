import Markdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";

import { cleanEditorMarkdown } from "@/lib/format/markdown";

import { cn } from "@/lib/utils";

/**
 * Authored Markdown, rendered.
 *
 * One renderer for the constructor's preview and, when step 5 arrives, for the
 * participant's screen. Two would guarantee that an author sees something
 * other than what a participant sees, and that the difference is discovered on
 * the day of a contest.
 *
 * It is a security boundary. The story is written by a contest manager — a
 * less trusted role than an administrator — and read by every participant
 * while they work. The defence is structural rather than a filter: this
 * produces React elements, never an HTML string, so there is no
 * `dangerouslySetInnerHTML` in the path and no sanitiser to be got wrong. Raw
 * HTML in the source is not parsed at all; `<script>` is four words and an
 * angle bracket, and stays that way.
 *
 * GitHub-flavoured Markdown for tables and strikethrough. Tables are the case
 * that decided against a contenteditable editor in the first place: a
 * round-tripping WYSIWYG is exactly where one quietly turns into HTML and
 * stops being editable as Markdown.
 */

/** Protocols a link may use. Anything else is a link that runs code. */
const SAFE = new Set(["http:", "https:", "mailto:"]);

/**
 * `react-markdown` already refuses dangerous protocols; this says so in the
 * repository rather than depending on a default staying a default across a
 * major version of somebody else's package.
 */
function safeUrl(url: string): string {
  try {
    return SAFE.has(new URL(url, "https://placeholder.invalid").protocol) ? url : "";
  } catch {
    return "";
  }
}

const components: Components = {
  // A story may cite a source. It opens away from the contest — a participant
  // is working under a timer and must not lose the page — and `noopener`
  // because a tab opened from here can otherwise reach back through
  // `window.opener`.
  a: ({ href, children, ...rest }) => (
    <a {...rest} href={href} target="_blank" rel="noopener noreferrer">
      {children}
    </a>
  ),
};

/**
 * The typography of the rendered story, held here rather than passed in.
 *
 * It is the other half of "one renderer": if the preview and the participant's
 * screen were styled at their call sites, they would drift, and the author
 * would be proofreading something other than what is read. `className` is for
 * layout — width, margins — and never for how prose looks.
 *
 * Serif at the narrative step, the same face and size the source box is typed
 * in, so what an author writes and what a reader gets are the same words at
 * the same weight.
 */
const PROSE = [
  "font-serif text-narrative text-ink",
  "[&>*+*]:mt-4",
  "[&_h1]:font-sans [&_h1]:text-h2 [&_h1]:text-ink [&_h1]:mt-8",
  "[&_h2]:font-sans [&_h2]:text-h3 [&_h2]:text-ink [&_h2]:mt-7",
  "[&_h3]:font-sans [&_h3]:text-row [&_h3]:text-ink [&_h3]:mt-6",
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
  // Cleaned on the way out as well as on the way in (see cleanEditorMarkdown).
  // The editor's `<br />` artefacts are stopped at the save now, but the
  // stories already written still carry them, and raw HTML is deliberately not
  // rendered here — so without this they arrive on a participant's screen as
  // the four characters, printed. Doing it here rather than with a migration
  // means nothing rewrites somebody's own text in the database.
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
