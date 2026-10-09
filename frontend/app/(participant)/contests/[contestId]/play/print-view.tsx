import { StoryText } from "@/components/product/story-text";
import type { PlayDictionary } from "./dictionary";


/**
 * Everything allowed to print: the contest's name, who printed it and when,
 * and the story. Nothing else is imported, so it renders on its own
 * (`renderToString`, no session or data) to test real pagination.
 *
 * `workspace.tsx` mounts it in a container shown only under `@media print`.
 * A plain flowing document, without the play screen's fixed `100dvh` layout
 * that would clip a print. Printing starts from the story tab
 * (side-panel.tsx's `printStory`), so this view has no control of its own.
 */
export function PrintView({
  contestTitle,
  participantName,
  date,
  storyMarkdown,
  dict,
}: {
  contestTitle: string;
  /** Empty when the identity could not be read; the byline then shows only the date. */
  participantName: string;
  /** Already formatted for display (lib/format/datetime.ts), not an ISO instant. */
  date: string;
  storyMarkdown: string;
  dict: PlayDictionary;
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
