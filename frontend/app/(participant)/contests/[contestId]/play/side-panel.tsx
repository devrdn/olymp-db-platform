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
 * Prints the story in place, naming the file the save dialog offers.
 * Browsers suggest `document.title` for "Save as PDF", so the title is
 * swapped to `story-{contestId}` before `window.print()` and restored on
 * `afterprint`, with a timer as backstop where that event never fires.
 * `restored` keeps the late path from overwriting a title changed since.
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
 * The panel beside the console: story, questions, notes and leaderboard as
 * tabs. Every tab stays mounted while hidden, so switching keeps an
 * unfinished answer, a scroll position and a pending notes save.
 *
 * Once there is a story, its tab offers two exports:
 *
 * - Markdown, a link to `.../play/story.md`, a Route Handler beside this
 *   route that cleans stored `<br />` with the one TypeScript implementation
 *   of that rule (lib/format/markdown.ts).
 * - Print, a button because nothing is navigated to or fetched. It prints the
 *   hidden copy `workspace.tsx` keeps for `@media print`.
 */
export function SidePanel({
  storyBody,
  storyCover,
  storyUnavailable,
  accountId,
  contestId,
  questionEntries,
  initialNotes,
  scoring = "points",
  icpcPenaltyMin = 20,
  dict,
  locale,
}: {
  storyBody: React.ReactNode;
  /** The picture above the story, rendered on the server (`story-cover.tsx`). */
  storyCover: React.ReactNode;
  storyUnavailable: string | null;
  /** Whose screen this is; the notes draft is keyed by it. */
  accountId: string | null;
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

  // Scroll the selected tab back into view when the selection changes; the
  // strip scrolls sideways.
  useEffect(() => {
    const selected = tabListWrapRef.current?.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]');
    selected?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
  }, [tab]);

  return (
    <Tabs value={tab} onValueChange={setTab} className="h-full min-h-0">
      {/* The four labels can be wider than the panel at its narrower widths.
          `overflow-x-auto` keeps the overflow inside the strip, and `min-w-0`
          lifts the flex item's automatic min-content floor. */}
      <div ref={tabListWrapRef} className="min-w-0">
        <TabsList className="overflow-x-auto">
          <TabsTrigger value="story" className="px-2">{t.story}</TabsTrigger>
          <TabsTrigger value="questions" className="px-2">{t.questions}</TabsTrigger>
          <TabsTrigger value="notes" className="px-2">{t.notes}</TabsTrigger>
          <TabsTrigger value="leaderboard" className="px-2">{dict.leaderboard.tab}</TabsTrigger>
        </TabsList>
      </div>
      {/* `fill={false}`: prose and a form scroll as ordinary blocks and need
          no flex container. */}
      {/* `relative` makes the scroll box the containing block of the `sr-only`
          labels inside (`position: absolute`); otherwise they escape to the page
          and make a one-viewport screen scroll. */}
      <TabsContent value="story" fill={false} className="relative overflow-y-auto p-4">
        {storyUnavailable !== null ? (
          <p className="text-body text-ink-2">{storyUnavailable}</p>
        ) : (
          <>
            {/* The picture heads the story (SPEC.md §10), above the export
                row. */}
            {storyCover}
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
      {/* The one tab whose child fills the height; the field scrolls inside
          itself. Still a scrolling containing block, for its `sr-only` status
          and for a panel shorter than the field's minimum. */}
      <TabsContent value="notes" className="relative overflow-y-auto">
        <NotesPanel accountId={accountId} contestId={contestId} initial={initialNotes} dict={dict} />
      </TabsContent>
      <TabsContent value="leaderboard" fill={false} className="relative overflow-y-auto p-4">
        <LeaderboardTab contestId={contestId} active={tab === "leaderboard"} dict={dict} locale={locale} />
      </TabsContent>
    </Tabs>
  );
}

/**
 * Reads the standings only while its tab is showing: the panel stays mounted,
 * and polling every fifteen seconds for a hidden table is waste.
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
