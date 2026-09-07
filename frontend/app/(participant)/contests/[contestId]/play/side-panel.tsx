"use client";

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

  return (
    <Tabs defaultValue="questions" className="h-full min-h-0">
      <TabsList>
        <TabsTrigger value="story">{t.story}</TabsTrigger>
        <TabsTrigger value="questions">{t.questions}</TabsTrigger>
      </TabsList>
      <TabsContent value="story" className="overflow-y-auto p-4">
        {storyUnavailable !== null ? (
          <p className="text-body text-ink-2">{storyUnavailable}</p>
        ) : (
          storyBody
        )}
      </TabsContent>
      <TabsContent value="questions" className="overflow-y-auto p-4">
        <QuestionsPanel contestId={contestId} items={questionEntries} dict={dict} />
      </TabsContent>
    </Tabs>
  );
}
