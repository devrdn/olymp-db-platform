"use client";

import { memo, useState } from "react";

import { cn } from "@/lib/utils";
import type { QuestionEntry } from "./questions-panel";
import type { QueryLogEntry } from "@/lib/api/querylog";
import type { PlayDictionary } from "./dictionary";
import type { GameSchema } from "@/lib/api/schema";
import type { Scoring } from "@/lib/api/contests";
import type { WorkspaceSnapshot } from "@/lib/api/workspace";
import type { Locale } from "@/lib/i18n/config";

import type { ConsoleState } from "./actions";
import { ConsoleEditor } from "./console";
import { QueryLogPanel } from "./query-log-panel";
import { ResultPanel } from "./result-panel";
import { PaneHandle, usePaneWidths } from "./pane-splitter";
import { SchemaPanel } from "./schema-panel";
import { SidePanel } from "./side-panel";

// Finding 5: a bottom-tab click sets state only in Workspace, but every
// child under it would still re-render on that state change unless it is
// memoised — the thousand-row result table, the log table, the whole side
// panel included, none of whose own props move when the only thing that
// changed is which tab is showing. Each of these takes
// nothing but values that are already stable across a tab click (Workspace's
// own state and its own unchanging props), so a shallow prop comparison is
// exactly the right amount of work to skip a reconciliation that buys
// nothing. QueryLogPanel is the one exception worth naming: its `active` prop
// does change when the bottom tab flips to or from "log", and that is meant
// to re-render it — memoising does not defeat that, it only stops the others
// from being dragged along for the ride.
const MemoResultPanel = memo(ResultPanel);
const MemoQueryLogPanel = memo(QueryLogPanel);
const MemoSidePanel = memo(SidePanel);
// The schema never changes for the length of a contest, so nothing about a
// tab click or a completed run has any business re-rendering its tree.
const MemoSchemaPanel = memo(SchemaPanel);

/**
 * The full-screen olympiad workspace: the console as the editor, a panel
 * below it for the last result and the query log, and a panel beside it for
 * the story and the questions.
 *
 * The bar above it — the contest's name and the clock — is deliberately not
 * here. `page.tsx` renders `PlayHeader` itself, above the `<Suspense>`
 * boundary this component sits inside, so the title and a running countdown
 * reach the participant in the first wave of the response rather than after
 * four API requests have settled (finding 2). Everything this component
 * draws depends on one of those four; the header depends on none of them.
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
 *
 * It also carries the print-only copy of the story (`PrintView`, reused
 * unchanged from what the old `.../play/print` route rendered — see
 * print-view.tsx's own doc). Printing used to mean leaving for that separate
 * route; it now happens on this screen, because the alternative — un-styling
 * this component's own fixed `100dvh` shape under `@media print` so the
 * story could flow instead of clip — is exactly the fragility that route was
 * created to avoid, on a tree that also holds the console, the schema panel,
 * the result table and the query log. So instead: the print copy sits beside
 * the interactive workspace the whole time, hidden on screen and shown only
 * to print (`hidden print:block`), and the workspace itself carries the
 * mirror image of that (`print:hidden`) — two classes rather than a
 * stylesheet that has to keep re-deriving "nothing here" for every panel
 * this screen grows next.
 *
 * That copy arrives as `printView`, already rendered by `page.tsx`, and this
 * file deliberately does not import `PrintView` itself. It used to, and this
 * is a client component: the import reached `StoryText`, which is
 * `react-markdown` and `remark-gfm` — 31.9 KiB gzipped of Markdown parser
 * measured in this route's own chunks, on the screen whose time-to-
 * interactive matters more than any other in the product. Worse, it was paid
 * twice: mounted the whole contest to build a subtree that is `display:none`
 * until somebody prints, the browser parsed the same story a second time on
 * every mount. `storyBody` beside it is a server-rendered node for exactly
 * the same reason (page.tsx's own comment); the print copy is now one too.
 */
export function Workspace({
  contestId,
  storyBody,
  printView,
  storyUnavailable,
  questionEntries,
  scoring = "points",
  icpcPenaltyMin = 20,
  schema,
  initialLog,
  workspace,
  locale,
  dict,
}: {
  contestId: string;
  storyBody: React.ReactNode;
  /** The print-only copy of the story, rendered on the server by `page.tsx` — see this component's own doc for why it is a node and not the Markdown behind it. Null exactly when there is no story to print (mirrors `storyUnavailable`). */
  printView: React.ReactNode;
  storyUnavailable: string | null;
  questionEntries: QuestionEntry[];
  /** The contest's own scoring mode, read from the summary `page.tsx` already loads — see `questions-panel.tsx` for what ICPC changes on this screen. Defaulted, like `icpcPenaltyMin` below, because most of this component's own tests predate ICPC scoring and have nothing to do with it. */
  scoring?: Scoring;
  /** Minutes added for a wrong attempt on a question later solved. Read only while `scoring` is `icpc`. */
  icpcPenaltyMin?: number;
  /** The game's shape, or null in a contest that hides it — see SchemaPanel. */
  schema: GameSchema | null;
  initialLog: { items: QueryLogEntry[]; total: number; failed: boolean };
  /**
   * The participant's notes and SQL tabs as the page read them, or null when
   * that read failed — the screen still works, and the notes say they could
   * not be loaded.
   */
  workspace: WorkspaceSnapshot | null;
  locale: Locale;
  dict: PlayDictionary;
}) {
  const t = dict.participant.play.workspace;

  // The latest run, lifted out of ConsoleEditor so ResultPanel — which lives
  // in a different subtree, inside a tab — can show it. ConsoleEditor's own
  // form and its useActionState never move; only this mirror of its result
  // does, which is what keeps a run from remounting the textarea.
  const [lastResult, setLastResult] = useState<ConsoleState>({ kind: "idle" });
  // Which SQL tab that result was run from. Kept beside the result rather
  // than read from the console: the participant goes on typing in another
  // tab while reading an answer, and the answer still belongs to the tab it
  // came from (§5).
  const [resultFrom, setResultFrom] = useState<string | null>(null);
  // Which tab of the bottom panel is showing. Controlled, rather than left to
  // Tabs' own uncontrolled state, so a completed run can switch to "Result"
  // by itself — the same reason a build's own output panel opens itself in
  // an editor: seeing what a query just did is the point of running it, and
  // a participant should not have to go looking for the tab that shows it.
  const [bottomTab, setBottomTab] = useState("result");
  const { containerRef, widths, commit } = usePaneWidths(contestId);

  return (
    <>
      {/* Print-only: on screen this is `display: none` (`hidden`), so it
          costs nothing visible and nothing interactive; `print:block` is the
          only rule that ever shows it. Absent its own content — rather than
          rendered with an empty story — when there is nothing to print,
          which mirrors `storyUnavailable`: the one control that opens a
          print (side-panel.tsx) does not render either in that state, so
          this is reachable only when there is a story. */}
      <div className="hidden print:block">{printView}</div>
      {/* The fixed, no-page-scroll VS Code shape is a `narrow:` (>=760px)
          decision: below that, a phone-sized screen doing a two-hour SQL
          olympiad is the edge case, and a tall, ordinarily-scrolling stack of
          sections (console, then its tabs, then the story/questions) serves it
          far better than clipping three panels into one viewport-height column
          — the same "collapse to one track" reasoning SPEC.md §5's mobile reset
          already applies everywhere else.

          What that fallback deliberately gives up is the *screen* being exactly
          one viewport tall: below 760px the page scrolls, section after
          section. What it does not give up is each panel's own scroll box. An
          earlier version left the result panel height-unconstrained here, on
          the reasoning that a scrolling page is the narrow answer to
          everything; measured, that put ten thousand pixels of result rows
          between the console and the questions on a 375px screen. The bottom
          panel therefore keeps a bound of its own below the breakpoint — see
          its own comment further down — so what scrolls the page is the list of
          sections, never the length of one query's answer.
          The viewport-height arithmetic itself now lives on `page.tsx`'s
          own shell, which is the element that holds the header and this
          together — including finding 7's correction that the app bar above
          this route is `h-12` (3rem) *plus* its own `border-b`, so 3rem
          alone is a pixel short and a "no page scroll" screen that scrolls
          by one pixel is still a screen that scrolls. What is left here is
          `narrow:flex-1`: take the rest of that height, and only from the
          breakpoint up — `flex-1` in a column whose height is its content
          would resolve against a zero basis and collapse.

          `print:hidden`: the mirror image of the container above — this is
          the tree `@media print` must never draw. */}
      <div className="flex min-h-0 flex-col print:hidden narrow:flex-1">
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
          ref={containerRef}
          style={
            {
              "--pane-schema": `${widths.schema}rem`,
              "--pane-side": `${widths.side}rem`,
            } as React.CSSProperties
          }
          className={cn(
            "grid min-h-0 grid-cols-1 narrow:flex-1",
            // The two hairlines between the panes are grid columns of their own,
            // so dragging one changes a width and never a margin.
            // Two panes from `narrow`, three only from `wide`. Measured: at a
            // 768px viewport the three-pane layout leaves the editor and the
            // result table 302px between them, which is a result table that
            // scrolls sideways on every query. The schema stacks below the
            // console until there is room for it beside.
            "narrow:grid-cols-[minmax(0,1fr)_1px_var(--pane-side)]",
            schema !== null &&
              "wide:grid-cols-[var(--pane-schema)_1px_minmax(0,1fr)_1px_var(--pane-side)]",
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
            // Order is declared per range, and every pane in the row declares
            // one — including the hairlines. `order` decides the sequence grid
            // auto-placement walks the children in, so one child left at the
            // default `order: 0` is placed *before* everything that carries a
            // number: the questions' own handle used to be that child, which
            // put it in the first column, pushed the console into the 1px
            // divider column, and left the editor 1px wide from 760px to
            // 1024px. Measured at a 768px viewport: console column 1x310,
            // editor 702x124 spilling 701px past it, document scrolling 448px
            // sideways.
            //
            // Below `narrow` the row is one track and the schema sits between
            // the console and the questions (order 2). From `narrow` to `wide`
            // the row is console | handle | questions and the schema spans a
            // second row underneath, so it has to be placed *last* (order 4) —
            // a full-width span placed earlier would break the row it was
            // meant to sit under.
            <div className="flex min-h-0 flex-col border-line max-wide:col-span-full max-wide:max-h-80 max-wide:border-t max-narrow:order-2 narrow:max-wide:order-4">
              <MemoSchemaPanel schema={schema} dict={dict} />
            </div>
          ) : null}
          {schema !== null ? (
            <PaneHandle
              label={t.panes.schema}
              property="--pane-schema"
              rem={widths.schema}
              direction={1}
              containerRef={containerRef}
              onResize={(rem) => commit({ ...widths, schema: rem })}
              className="max-wide:hidden"
            />
          ) : null}
          {/* The console side: the editor on top, always visible, and the
            result/log tabs below it, sized 55/45 of this column's height —
            fixed on a workspace-height screen; on the narrow fallback each
            gets a comfortable minimum instead of a share of a height that no
            longer applies. */}
          {/* `grid-cols-1` is load-bearing, not decoration. Without an explicit
            column this grid gets one implicit `auto` track, and an auto track
            is floored at its content's *max-content* width — which here is
            the result table's own natural width, whatever the last query
            made it. Measured before: the track came out 701.703px wide at
            every viewport, so at 375px the document scrolled 326px sideways,
            and at 1024px the editor and the result painted 144px over the
            questions beside them. `grid-cols-1` is
            `repeat(1, minmax(0, 1fr))` — a track that may not exceed its
            container, which is what puts the sideways scrolling back inside
            the result table's own scroll box where it belongs. */}
          <div className="grid min-h-0 grid-cols-1 grid-rows-[minmax(0,11fr)_minmax(0,9fr)] border-line max-wide:order-1 max-narrow:grid-rows-none max-narrow:border-b">
            {/* A flex column, not a block: the console's form claims the cell
              with flex-1, and flex-1 is inert inside a block parent — which
              left the editor with no height at all. */}
            <div className="flex min-h-0 flex-col border-b border-line max-narrow:min-h-80">
              <ConsoleEditor
                contestId={contestId}
                dict={dict}
                // The same array on every render, so the memoised panels
                // around it are not disturbed by it.
                tabs={workspace?.tabs ?? null}
                actions={
                  // The design's toolbar carries the query log as a button
                  // rather than a tab strip over the result
                  // (docs/design/preview.html): the result is what the pane
                  // below is *for*, and a tab strip above it says the two are
                  // equals. They are not — one is the answer to what was just
                  // typed, the other is a record of what already happened.
                  <>
                    <ToolbarButton
                      active={bottomTab === "result"}
                      onClick={() => setBottomTab("result")}
                    >
                      {t.tabs.result}
                    </ToolbarButton>
                    <ToolbarButton
                      active={bottomTab === "log"}
                      onClick={() => setBottomTab("log")}
                    >
                      {t.tabs.log}
                    </ToolbarButton>
                  </>
                }
                onResult={(state, source) => {
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
                    if (source) setResultFrom(source.tabTitle);
                    setBottomTab("result");
                  }
                }}
              />
            </div>

            {/* Both stay mounted: switching to the log and back must not lose
              the result that is on screen, nor the log's own scroll position.
              Hidden with a class rather than the `hidden` attribute, because
              these panes are flex containers and `display:flex` would win
              over the attribute's own `display:none`.

              `max-narrow:max-h-[60svh]` is what keeps the result a scroll box
              on the narrow fallback too. Without a bound there the panel is
              in ordinary document flow and lays a result out at its natural
              height: measured at 375px, a forty-row result made the page
              11,487px tall, and the participant had to scroll roughly ten
              thousand pixels of rows — around forty flicks at the thousand
              rows the runner actually allows — to reach the questions
              underneath, which are the thing they have to answer. Bounding
              the panel rather than the page keeps SPEC.md §5's own reset
              ("collapse to one track, keep every panel"): every panel is
              still there, in one track, in the same order. `svh` rather than
              `dvh` so a phone's disappearing URL bar does not resize the
              box under a finger that is scrolling it. */}
            <div className="flex min-h-0 flex-col max-narrow:max-h-[60svh]">
              <div
                className={cn(
                  "min-h-0 flex-1 overflow-hidden",
                  bottomTab === "result" ? "flex flex-col" : "hidden",
                )}
              >
                <MemoResultPanel state={lastResult} sourceTitle={resultFrom} dict={dict} />
              </div>
              <div
                className={cn(
                  "min-h-0 flex-1 overflow-hidden",
                  bottomTab === "log" ? "flex flex-col" : "hidden",
                )}
              >
                <MemoQueryLogPanel
                  contestId={contestId}
                  initial={initialLog}
                  active={bottomTab === "log"}
                  locale={locale}
                  dict={dict}
                />
              </div>
            </div>
          </div>

          <PaneHandle
            label={t.panes.side}
            property="--pane-side"
            rem={widths.side}
            direction={-1}
            containerRef={containerRef}
            onResize={(rem) => commit({ ...widths, side: rem })}
            // `order-2`, not the default: this handle is a child of the pane
            // grid like any other, and a child with no order is placed ahead of
            // every child that has one. See the schema pane's own comment for
            // what that cost between 760px and 1024px.
            className="max-narrow:hidden max-wide:order-2"
          />

          {/* The story/questions side: below the console column on a narrow
            screen rather than beside it — the same "collapse to one track,
            keep every panel" reset the rest of the direction uses
            (SPEC.md §5's own mobile reset) — one instance, one state, so a
            half-typed answer survives a resize the same way it survives a
            tab switch. */}
          <div className="min-h-0 max-wide:order-3 max-narrow:min-h-100 max-narrow:border-t max-narrow:border-line">
            <MemoSidePanel
              storyBody={storyBody}
              storyUnavailable={storyUnavailable}
              contestId={contestId}
              questionEntries={questionEntries}
              // The same object on every render, so the memoised panel is
              // not disturbed by it.
              initialNotes={workspace?.notes ?? null}
              scoring={scoring}
              icpcPenaltyMin={icpcPenaltyMin}
              dict={dict}
              locale={locale}
            />
          </div>
        </div>
      </div>
    </>
  );
}

/**
 * One of the small buttons at the right end of the console's toolbar.
 *
 * Not a Tabs trigger. The design's toolbar is a row of quiet buttons — the
 * only filled thing on it is Run — and marking the current one with a wash
 * rather than an underline is what keeps the row reading as controls instead
 * of as a second navigation.
 */
function ToolbarButton({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        "rounded-full px-2.5 py-1 text-control-sm transition-colors duration-(--t-input) ease-standard",
        active ? "bg-sunk text-ink" : "text-ink-2 hover:bg-sunk hover:text-ink",
      )}
    >
      {children}
    </button>
  );
}
