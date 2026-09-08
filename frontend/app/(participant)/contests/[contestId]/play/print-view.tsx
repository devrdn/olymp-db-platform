import { StoryText } from "@/components/product/story-text";
import type { Dictionary } from "@/lib/i18n/dictionary";


/**
 * Everything that is allowed to print: the contest's name, who printed it
 * and when, and the story. No console, no schema tree, no result table, no
 * navigation — not hidden by a stylesheet, simply never imported, which is
 * what makes it possible to render this component on its own (`renderToString`,
 * no cookies, no session, no data fetching) to check the one thing jsdom
 * cannot: how a real browser paginates it.
 *
 * `workspace.tsx` mounts this once, inside a container that is `hidden` on
 * screen and `print:block` only under `@media print`, beside the interactive
 * workspace it hides the mirror image of. It used to be a whole separate
 * route instead (`.../play/print`) — see workspace.tsx's own doc for why
 * that was given up.
 *
 * A plain document, deliberately — no `100dvh`, no internal scroll box. The
 * play screen this story is normally read on is fixed and full-bleed on
 * purpose (`workspace.tsx`'s own doc), which is exactly what would clip a
 * print to one page of cut-off panels; this view never adopts that layout in
 * the first place; instead it flows the ordinary way a browser already knows
 * how to paginate. Printing is started from the button beside the
 * story tab (side-panel.tsx's `printStory`), which is why this view carries
 * no control of its own: a button hidden on screen and `print:hidden` once
 * printing starts is a button nobody can ever press.
 */
export function PrintView({
  contestTitle,
  participantName,
  date,
  storyMarkdown,
  dict,
}: {
  contestTitle: string;
  /** Empty when the identity behind this request could not be read — see the play screen's own dict entries (`participant.play.print`). */
  participantName: string;
  /** Already formatted for display (lib/format/datetime.ts), not an ISO instant. */
  date: string;
  storyMarkdown: string;
  dict: Dictionary;
}) {
  const t = dict.participant.play;

  return (
    <div className="mx-auto max-w-narrative px-6 py-10 print:max-w-none print:px-0 print:py-0">
      <div className="mb-6 flex items-start justify-between gap-4 print:hidden">
      </div>
      <header className="mb-8 flex flex-col gap-1 border-b border-line pb-4">
        <h1 className="text-h2 text-ink">{contestTitle}</h1>
        <p className="text-small text-ink-2">
          {participantName
            ? t.print.by.replace("{name}", participantName).replace("{date}", date)
            : t.print.byUnknown.replace("{date}", date)}
        </p>
      </header>
      <h2 className="mb-3 text-h3 text-ink">{t.story.heading}</h2>
      <StoryText markdown={storyMarkdown} />
    </div>
  );
}
