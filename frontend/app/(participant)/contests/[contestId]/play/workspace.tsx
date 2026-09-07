"use client";

import { memo, useState } from "react";

import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { cn } from "@/lib/utils";
import type { QuestionEntry } from "./questions-panel";
import type { QueryLogEntry } from "@/lib/api/querylog";
import type { Dictionary } from "@/lib/i18n/dictionary";
import type { GameSchema } from "@/lib/api/schema";
import type { Locale } from "@/lib/i18n/config";

import type { ConsoleState } from "./actions";
import { ConsoleEditor } from "./console";
import { PlayHeader } from "./play-header";
import { QueryLogPanel } from "./query-log-panel";
import { ResultPanel } from "./result-panel";
import { SchemaPanel } from "./schema-panel";
import { SidePanel } from "./side-panel";

// Finding 5: a bottom-tab click sets state only in Workspace, but every
// child under it would still re-render on that state change unless it is
// memoised — the thousand-row result table, the log table, the whole side
// panel and the header included, none of whose own props move when the only
// thing that changed is which tab is showing. Each of these four takes
// nothing but values that are already stable across a tab click (Workspace's
// own state and its own unchanging props), so a shallow prop comparison is
// exactly the right amount of work to skip a reconciliation that buys
// nothing. QueryLogPanel is the one exception worth naming: its `active` prop
// does change when the bottom tab flips to or from "log", and that is meant
// to re-render it — memoising does not defeat that, it only stops the *other*
// three from being dragged along for the ride.
const MemoPlayHeader = memo(PlayHeader);
const MemoResultPanel = memo(ResultPanel);
const MemoQueryLogPanel = memo(QueryLogPanel);
const MemoSidePanel = memo(SidePanel);
// The schema never changes for the length of a contest, so nothing about a
// tab click or a completed run has any business re-rendering its tree.
const MemoSchemaPanel = memo(SchemaPanel);

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
  schema,
  initialLog,
  locale,
  dict,
}: {
  contestId: string;
  title: string;
  storyBody: React.ReactNode;
  storyUnavailable: string | null;
  questionEntries: QuestionEntry[];
  /** The game's shape, or null in a contest that hides it — see SchemaPanel. */
  schema: GameSchema | null;
  initialLog: { items: QueryLogEntry[]; total: number; failed: boolean };
  locale: Locale;
  dict: Dictionary;
}) {
  const t = dict.participant.play.workspace;

  // The latest run, lifted out of ConsoleEditor so ResultPanel — which lives
  // in a different subtree, inside a tab — can show it. ConsoleEditor's own
  // form and its useActionState never move; only this mirror of its result
  // does, which is what keeps a run from remounting the textarea.
  const [lastResult, setLastResult] = useState<ConsoleState>({ kind: "idle" });
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
    //
    // The bounded scroll box this layout gives the result table (finding 1
    // of the earlier review) is therefore a `narrow:`-and-up property too:
    // below the breakpoint the result panel is not height-constrained at
    // all, so a full thousand-row table lays out at its natural height in
    // the document flow — measured at over 33,000px tall — and the whole
    // page scrolls instead of a fixed-height box scrolling inside it. That
    // is this fallback working as designed, not the scroll fix failing
    // below 760px; do not read a very tall narrow-mode page as the bug
    // returning.
    // Finding 7: the app bar this route sits below (`AppBar`) is `h-12`
    // (3rem) *plus* its own `border-b` — 3rem alone is one pixel short of
    // its real height, and a "no page scroll" screen that scrolls by one
    // pixel is still a screen that scrolls.
    <div className="flex min-h-0 flex-col narrow:h-[calc(100dvh-3rem-1px)]">
      <MemoPlayHeader contestId={contestId} title={title} waitingForStart={false} dict={dict} />

      {/* The design's three panes: the schema down the left, the editor and
          its result in the middle, the story and the questions on the right
          (docs/design/preview.html, "SQL-консоль"). The two side columns are
          fixed widths and the middle one takes what is left, because the
          middle is the only one whose content has no natural width — a
          result table is as wide as the query made it.

          Two columns, not three, in a contest that closed its catalogues:
          the panel is absent rather than empty there, and leaving its column
          in place would spend a fifth of the screen saying nothing. */}
      <div
        className={cn(
          "grid min-h-0 grid-cols-1 narrow:flex-1",
          schema !== null
            ? "narrow:grid-cols-[13.25rem_minmax(0,1fr)_15.75rem]"
            : "narrow:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)]",
        )}
      >
        {schema !== null ? (
          // Source order is the wide layout's own order, so the tab order a
          // participant walks matches what they see. Below the breakpoint
          // that would put a reference panel above the thing they came to
          // type in, so there — and only there — it is moved after the
          // console.
          //
          // A flex column, not a block. The panel claims the cell with
          // `flex-1`, and `flex-1` is inert inside a block parent — the exact
          // defect that left the editor with no height at all (see the
          // console cell below). With `max-h-80` on the narrow fallback the
          // column has a bounded height rather than a definite one, which is
          // why the panel's own scroller sits inside it and not here.
          <div className="flex min-h-0 flex-col border-line narrow:border-r max-narrow:order-2 max-narrow:max-h-80 max-narrow:border-t">
            <MemoSchemaPanel schema={schema} dict={dict} />
          </div>
        ) : null}
        {/* The console side: the editor on top, always visible, and the
            result/log tabs below it, sized 55/45 of this column's height —
            fixed on a workspace-height screen; on the narrow fallback each
            gets a comfortable minimum instead of a share of a height that no
            longer applies. */}
        <div className="grid min-h-0 grid-rows-[minmax(0,11fr)_minmax(0,9fr)] border-line narrow:border-r max-narrow:order-1 max-narrow:grid-rows-none max-narrow:border-b">
          {/* A flex column, not a block: the console's form claims the cell
              with flex-1, and flex-1 is inert inside a block parent — which
              left the editor with no height at all. */}
          <div className="flex min-h-0 flex-col border-b border-line max-narrow:min-h-80">
            <ConsoleEditor
              contestId={contestId}
              dict={dict}
              onResult={(state) => {
                setLastResult(state);
                // Finding 3: this used to also bump a token that made
                // QueryLogPanel refetch on every completed run. AdmitRead
                // shares its per-minute budget with Run, so that refetch
                // spent one of the participant's own query slots — and it
                // did so for a tab that, because of the very next line, had
                // just been switched away from anyway. QueryLogPanel now
                // refreshes itself off the `active` prop below, only on the
                // transition into actually being shown.
                if (state.kind !== "idle") {
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
              <MemoResultPanel state={lastResult} dict={dict} />
            </TabsContent>
            <TabsContent value="log" className="min-h-0 overflow-hidden">
              <MemoQueryLogPanel
                contestId={contestId}
                initial={initialLog}
                active={bottomTab === "log"}
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
        <div className="min-h-0 max-narrow:order-3 max-narrow:min-h-100 max-narrow:border-t max-narrow:border-line">
          <MemoSidePanel
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
