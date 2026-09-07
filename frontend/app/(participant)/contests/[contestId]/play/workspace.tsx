"use client";

import { useState } from "react";

import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import type { QuestionEntry } from "./questions-panel";
import type { QueryLogEntry } from "@/lib/api/querylog";
import type { Dictionary } from "@/lib/i18n/dictionary";
import type { Locale } from "@/lib/i18n/config";

import type { ConsoleState } from "./actions";
import { ConsoleEditor } from "./console";
import { PlayHeader } from "./play-header";
import { QueryLogPanel } from "./query-log-panel";
import { ResultPanel } from "./result-panel";
import { SidePanel } from "./side-panel";

/**
 * The full-screen olympiad workspace: the console as the editor, a panel
 * below it for the last result and the query log, a panel beside it for the
 * story and the questions, and a thin bar above everything for the
 * contest's name and the clock.
 *
 * This is the one screen in the product that goes full-bleed — no hatched
 * side fields — a deliberate exception to `docs/design/SPEC.md` §5, recorded
 * there rather than made silently: during an olympiad every pixel goes to
 * the data, exactly the reasoning §5 already gives the content column its
 * own width. `page.tsx` renders this outside `Band` for exactly that reason.
 *
 * Proportions are fixed, not draggable. A resizable split was in scope for
 * this task — nobody asked for the opposite — and was set aside on purpose:
 * dragging a divider smoothly needs its own pointer-event plumbing
 * (`requestAnimationFrame`-throttled resize, a persisted width, a keyboard
 * equivalent for the same accessibility reasons `Tabs` cares about), and none
 * of that touches what the plan actually asks to be smooth — switching a tab,
 * and the clock. A fixed layout has zero chance of jank because there is
 * nothing to compute, and the console — the thing being typed into — keeps
 * the larger share of the width either way.
 */
export function Workspace({
  contestId,
  title,
  storyBody,
  storyUnavailable,
  questionEntries,
  initialLog,
  locale,
  dict,
}: {
  contestId: string;
  title: string;
  storyBody: React.ReactNode;
  storyUnavailable: string | null;
  questionEntries: QuestionEntry[];
  initialLog: { items: QueryLogEntry[]; total: number };
  locale: Locale;
  dict: Dictionary;
}) {
  const t = dict.participant.play.workspace;

  // The latest run, lifted out of ConsoleEditor so ResultPanel — which lives
  // in a different subtree, inside a tab — can show it. ConsoleEditor's own
  // form and its useActionState never move; only this mirror of its result
  // does, which is what keeps a run from remounting the textarea.
  const [lastResult, setLastResult] = useState<ConsoleState>({ kind: "idle" });
  // Bumped once per completed run (never while one is in flight — see
  // ConsoleEditor's own doc on when onResult fires), so QueryLogPanel knows
  // to refetch: a run that reached the database wrote a row, and one that was
  // refused before the database saw it did not, but re-asking either way is
  // one cheap request against a wrong guess about which happened.
  const [logRefreshToken, setLogRefreshToken] = useState(0);
  // Which tab of the bottom panel is showing. Controlled, rather than left to
  // Tabs' own uncontrolled state, so a completed run can switch to "Result"
  // by itself — the same reason a build's own output panel opens itself in
  // an editor: seeing what a query just did is the point of running it, and
  // a participant should not have to go looking for the tab that shows it.
  const [bottomTab, setBottomTab] = useState("result");

  return (
    // The fixed, no-page-scroll VS Code shape is a `narrow:` (>=760px)
    // decision: below that, a phone-sized screen doing a two-hour SQL
    // olympiad is the edge case, and a tall, ordinarily-scrolling stack of
    // sections (console, then its tabs, then the story/questions) serves it
    // far better than clipping three panels into one viewport-height column
    // — the same "collapse to one track" reasoning SPEC.md §5's mobile reset
    // already applies everywhere else.
    <div className="flex min-h-0 flex-col narrow:h-[calc(100dvh-3rem)]">
      <PlayHeader contestId={contestId} title={title} waitingForStart={false} dict={dict} />

      <div className="grid min-h-0 grid-cols-1 narrow:flex-1 narrow:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)]">
        {/* The console side: the editor on top, always visible, and the
            result/log tabs below it, sized 55/45 of this column's height —
            fixed on a workspace-height screen; on the narrow fallback each
            gets a comfortable minimum instead of a share of a height that no
            longer applies. */}
        <div className="grid min-h-0 grid-rows-[minmax(0,11fr)_minmax(0,9fr)] border-line narrow:border-r max-narrow:grid-rows-none max-narrow:border-b">
          <div className="min-h-0 border-b border-line max-narrow:min-h-80">
            <ConsoleEditor
              contestId={contestId}
              dict={dict}
              onResult={(state) => {
                setLastResult(state);
                if (state.kind !== "idle") {
                  setLogRefreshToken((v) => v + 1);
                  setBottomTab("result");
                }
              }}
            />
          </div>
          <Tabs
            value={bottomTab}
            onValueChange={(value) => setBottomTab(String(value))}
            className="min-h-0 max-narrow:min-h-80"
          >
            <TabsList>
              <TabsTrigger value="result">{t.tabs.result}</TabsTrigger>
              <TabsTrigger value="log">{t.tabs.log}</TabsTrigger>
            </TabsList>
            <TabsContent value="result" className="min-h-0 overflow-hidden">
              <ResultPanel state={lastResult} dict={dict} />
            </TabsContent>
            <TabsContent value="log" className="min-h-0 overflow-hidden">
              <QueryLogPanel
                contestId={contestId}
                initial={initialLog}
                refreshToken={logRefreshToken}
                locale={locale}
                dict={dict}
              />
            </TabsContent>
          </Tabs>
        </div>

        {/* The story/questions side: below the console column on a narrow
            screen rather than beside it — the same "collapse to one track,
            keep every panel" reset the rest of the direction uses
            (SPEC.md §5's own mobile reset) — one instance, one state, so a
            half-typed answer survives a resize the same way it survives a
            tab switch. */}
        <div className="min-h-0 max-narrow:min-h-100 max-narrow:border-t max-narrow:border-line">
          <SidePanel
            storyBody={storyBody}
            storyUnavailable={storyUnavailable}
            contestId={contestId}
            questionEntries={questionEntries}
            dict={dict}
          />
        </div>
      </div>
    </div>
  );
}
