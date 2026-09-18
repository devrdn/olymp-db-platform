"use client";

import { useEffect, useRef, useState } from "react";

import { ExportMenu } from "@/components/product/export-menu";
import { StandingsView } from "@/components/product/standings";
import { useStandings } from "@/components/product/use-standings";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import type { Scoring } from "@/lib/api/contests";
import type { WorkspaceNotes } from "@/lib/api/workspace";
import { fetchStandingsAction } from "./actions";
import type { PlayDictionary } from "./dictionary";

import { NotesPanel } from "./notes-panel";
import { QuestionsPanel, type QuestionEntry } from "./questions-panel";

/**
 * Prints the story in place, and names the file the save dialog offers.
 *
 * Chrome, Edge and Safari all suggest `document.title` as the filename for
 * "Save as PDF" — there is no other web API for naming what a print
 * produces — so the title is swapped to `story-{contestId}` immediately
 * before `window.print()` and put back once the print is done.
 *
 * `afterprint` is the ordinary way to know a print finished (Chrome,
 * Firefox), but it does not fire on every browser (older Safari, some
 * in-app webviews) — a plain timer is the defensive backstop that restores
 * the title even there. `restored` guards both paths against firing twice:
 * without it, a late timer that ran after `afterprint` already restored the
 * title could stomp a title change made in between (a navigation, another
 * print). The one browser this cannot help — one that fires neither
 * `afterprint` nor gives the timer long enough to matter, because the
 * dialog stayed open past it — is why the timeout is generous rather than
 * tight: it exists to catch a browser that never restores it at all, not to
 * race the dialog closing.
 */
function printStory(contestId: string) {
  const previousTitle = document.title;

  let restored = false;
  const restore = () => {
    if (restored) return;
    restored = true;
    document.title = previousTitle;
    window.removeEventListener("afterprint", restore);
  };

  window.addEventListener("afterprint", restore);
  window.setTimeout(restore, 2000);

  document.title = `story-${contestId}`;
  window.print();
}

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
 * - Print, a button rather than a link: nothing here is a URL to navigate
 *   to or a file to fetch, so neither `download` (every other `ExportMenu`
 *   link carries it) nor a plain `href` says the right thing. It opens the
 *   browser's own print dialog on this same screen — `workspace.tsx` keeps a
 *   copy of the story hidden until `@media print` for exactly this button to
 *   reveal — rather than sending the participant to a separate route the
 *   way this used to work; see workspace.tsx's own doc for why that was
 *   fragile. `printStory` below is also what makes the file the dialog
 *   offers to save come out named `story-{contestId}`, since that is read
 *   from `document.title` and nothing else names it.
 *
 * The notes tab, right after the questions, is the participant's own
 * autosaved field (`NotesPanel`). It keeps its state to itself, so typing
 * there never re-renders this panel or the tabs beside it; and like every
 * tab here it stays mounted while hidden, so a save due when the
 * participant switches away still leaves.
 */
export function SidePanel({
  storyBody,
  storyUnavailable,
  contestId,
  questionEntries,
  initialNotes,
  scoring = "points",
  icpcPenaltyMin = 20,
  dict,
  locale,
}: {
  storyBody: React.ReactNode;
  storyUnavailable: string | null;
  contestId: string;
  questionEntries: QuestionEntry[];
  /** The notes as the page read them, or null when that read failed. */
  initialNotes: WorkspaceNotes | null;
  scoring?: Scoring;
  icpcPenaltyMin?: number;
  dict: PlayDictionary;
  locale: string;
}) {
  const t = dict.participant.play.workspace.tabs;
  const storyT = dict.participant.play.workspace.story;
  const [tab, setTab] = useState("questions");
  const tabListWrapRef = useRef<HTMLDivElement>(null);

  // The strip can be scrolled past the selected tab (the point of
  // `overflow-x-auto` below) — bring it back into view whenever the
  // selection changes, whether that came from a click or the keyboard.
  useEffect(() => {
    const selected = tabListWrapRef.current?.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]');
    selected?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
  }, [tab]);

  return (
    <Tabs value={tab} onValueChange={setTab} className="h-full min-h-0">
      {/* Four tabs — five with the leaderboard's own label, longer still in
          Russian — do not fit the panel's own width at every point the
          divider can be dragged to (`--pane-side`, clamped 8-32rem in
          pane-splitter.tsx); measured at 1440, 1024 and 768px, the strip ran
          54px past the panel and took the whole page's scrollbar with it.
          `overflow-x-auto` on the list keeps that overflow inside the strip
          instead, and `min-w-0` on the div wrapping it gives up flexbox's
          own floor on a flex item's width — its *automatic* minimum, absent
          this, is the content's min-content size, which is exactly what the
          four labels exceeded. */}
      <div ref={tabListWrapRef} className="min-w-0">
        <TabsList className="overflow-x-auto">
          <TabsTrigger value="story" className="px-2">{t.story}</TabsTrigger>
          <TabsTrigger value="questions" className="px-2">{t.questions}</TabsTrigger>
          <TabsTrigger value="notes" className="px-2">{t.notes}</TabsTrigger>
          <TabsTrigger value="leaderboard" className="px-2">{dict.leaderboard.tab}</TabsTrigger>
        </TabsList>
      </div>
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
              <button
                type="button"
                onClick={() => printStory(contestId)}
                className="text-small text-accent underline underline-offset-4"
              >
                {storyT.print}
              </button>
            </div>
            {storyBody}
          </>
        )}
      </TabsContent>
      <TabsContent value="questions" fill={false} className="relative overflow-y-auto p-4">
        <QuestionsPanel
          contestId={contestId}
          items={questionEntries}
          scoring={scoring}
          icpcPenaltyMin={icpcPenaltyMin}
          dict={dict}
        />
      </TabsContent>
      {/* The one tab here whose child fills the height: the field grows to
          the panel and scrolls inside itself. The tab still scrolls, and is
          still a containing block, for the same reason as the others: its
          status line is partly `sr-only`, and a panel shorter than the
          field's minimum height has to scroll rather than spill. */}
      <TabsContent value="notes" className="relative overflow-y-auto">
        <NotesPanel contestId={contestId} initial={initialNotes} dict={dict} />
      </TabsContent>
      <TabsContent value="leaderboard" fill={false} className="relative overflow-y-auto p-4">
        <LeaderboardTab contestId={contestId} active={tab === "leaderboard"} dict={dict} locale={locale} />
      </TabsContent>
    </Tabs>
  );
}

/**
 * The table, read only while its tab is the one showing: the panel stays
 * mounted behind the other tabs, and a table nobody is looking at is a request
 * every fifteen seconds for nothing.
 */
function LeaderboardTab({
  contestId,
  active,
  dict,
  locale,
}: {
  contestId: string;
  active: boolean;
  dict: PlayDictionary;
  locale: string;
}) {
  const { standings, failed } = useStandings({ load: () => fetchStandingsAction(contestId), active });

  if (!standings) {
    return <p className="text-body text-ink-2">{failed ? dict.leaderboard.failed : dict.leaderboard.heading}</p>;
  }
  return <StandingsView standings={standings} dict={dict} locale={locale} variant="panel" failed={failed} />;
}
