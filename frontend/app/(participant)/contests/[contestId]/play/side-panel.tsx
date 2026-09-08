"use client";

import { ExportMenu } from "@/components/product/export-menu";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { QuestionsPanel, type QuestionEntry } from "./questions-panel";

/**
 * The panel beside the console: the story, and the questions with their
 * answer fields, behind two tabs rather than stacked one above the other.
 *
 * Both panels stay mounted the whole time (`TabsContent`'s own default) —
 * switching from "Story" to "Questions" and back must not lose an
 * in-progress answer or a scroll position, the same guarantee the bottom
 * panel gives the query result and the log.
 *
 * The story tab also carries the two ways to take the story away, offered
 * only once there is a story to take (the same rule `ExportMenu`'s own doc
 * gives the query log's link — a screen reader announces a group heading
 * with nothing under it all the same):
 *
 * - Markdown, a plain download link to `.../play/story.md` — a Next.js Route
 *   Handler beside this route, not the Go API directly. See that route's own
 *   doc for why: the Markdown a story is stored as can still carry a WYSIWYG
 *   editor's literal `<br />` (lib/format/markdown.ts), and cleaning it needs
 *   the one TypeScript implementation of that rule, not a second one ported
 *   into Go for this one file.
 * - Print, a plain navigation link to `.../play/print` — a separate route
 *   (see that route's own doc) rather than a print stylesheet laid over this
 *   very screen, and rather than a member of `ExportMenu`'s own list: what it
 *   offers is a page to look at and print, not a file this control fetches,
 *   so the `download` attribute every `ExportMenu` link carries would be the
 *   wrong instruction here. `target="_blank"` for the same reason StoryText's
 *   own citation links use it — a participant working under a timer must not
 *   lose this screen to a tab that only exists to be printed.
 */
export function SidePanel({
  storyBody,
  storyUnavailable,
  contestId,
  questionEntries,
  dict,
}: {
  storyBody: React.ReactNode;
  storyUnavailable: string | null;
  contestId: string;
  questionEntries: QuestionEntry[];
  dict: Dictionary;
}) {
  const t = dict.participant.play.workspace.tabs;
  const storyT = dict.participant.play.workspace.story;

  return (
    <Tabs defaultValue="questions" className="h-full min-h-0">
      <TabsList>
        <TabsTrigger value="story">{t.story}</TabsTrigger>
        <TabsTrigger value="questions">{t.questions}</TabsTrigger>
      </TabsList>
      {/* Neither tab has a child that needs to fill the panel's height —
          the story is prose and the questions are a form, both laid out
          and scrolled the ordinary block way — so `fill={false}` keeps
          `TabsContent` a plain block box rather than a flex container
          (finding 3: forcing `flex-col` here bought nothing and turned
          every direct child into a flex item). */}
      {/* `relative` is not styling: `sr-only` is `position: absolute`, and an
          absolutely-positioned descendant of a *static* scroll box is not
          clipped by it — its containing block is whatever positioned ancestor
          comes next, which here was the page itself. Every question in the
          list carries two of them (its number, and a choice question's
          legend), so the last question's hidden label sat at the page's own
          coordinates however far down the panel it had scrolled to, and
          dragged the document's scroll area with it. Measured at 1920x1080
          with five questions: a screen that is supposed to be exactly one
          viewport tall scrolled 263px, all of it empty, and the figure grows
          with the number of questions. Making the scroll box a containing
          block is what puts those labels back inside it. */}
      <TabsContent value="story" fill={false} className="relative overflow-y-auto p-4">
        {storyUnavailable !== null ? (
          <p className="text-body text-ink-2">{storyUnavailable}</p>
        ) : (
          <>
            <div className="mb-4 flex items-center justify-between gap-3">
              <ExportMenu
                heading={storyT.export.heading}
                formats={[
                  {
                    format: "Markdown",
                    href: `/contests/${contestId}/play/story.md`,
                    label: storyT.export.label,
                  },
                ]}
              />
              <a
                href={`/contests/${contestId}/play/print`}
                target="_blank"
                rel="noopener noreferrer"
                className="text-small text-accent underline underline-offset-4"
              >
                {storyT.print}
              </a>
            </div>
            {storyBody}
          </>
        )}
      </TabsContent>
      <TabsContent value="questions" fill={false} className="relative overflow-y-auto p-4">
        <QuestionsPanel contestId={contestId} items={questionEntries} dict={dict} />
      </TabsContent>
    </Tabs>
  );
}
