"use client";

import { memo, useCallback, useEffect, useMemo, useState } from "react";

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
import { usePanelVisibility, useSchemaPanel } from "./panel-toggles";
import { QueryLogPanel } from "./query-log-panel";
import { ResultPanel } from "./result-panel";
import { PaneHandle, SHARE_BOUNDS, WIDTH_BOUNDS, useConsoleRows, usePaneWidths } from "./pane-splitter";
import { SchemaPanel } from "./schema-panel";
import { SidePanel } from "./side-panel";
import { purgeForeignDrafts } from "./use-autosave";
import { useSignals } from "./use-signals";

// A bottom-tab click changes state only here; memoising the heavy children
// keeps them from re-rendering when none of their props moved. QueryLogPanel
// still re-renders when its `active` prop flips, as intended.
const MemoResultPanel = memo(ResultPanel);
const MemoQueryLogPanel = memo(QueryLogPanel);
const MemoSidePanel = memo(SidePanel);
// The schema is fixed for the whole contest.
const MemoSchemaPanel = memo(SchemaPanel);

/**
 * The full-screen olympiad workspace: the console, a panel below it for the
 * last result and the query log, and a panel beside it for the story and the
 * questions.
 *
 * Full-bleed, an exception to `docs/design/SPEC.md` §5 recorded there: during
 * an olympiad every pixel goes to the data. Every pane edge is draggable and
 * remembered (`pane-splitter.tsx`).
 *
 * The print copy of the story sits beside the workspace, hidden on screen
 * (`hidden print:block`), while the workspace is `print:hidden`; un-styling
 * this fixed `100dvh` layout for print would be fragile. It arrives as a
 * server-rendered `printView` node.
 */
export function Workspace({
  accountId,
  contestId,
  storyBody,
  storyCover,
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
  /**
   * The signed-in account, or null when it could not be read. Drafts in this
   * browser are keyed by it, and other accounts' drafts are swept on mount:
   * lab machines are shared.
   */
  accountId: string | null;
  contestId: string;
  storyBody: React.ReactNode;
  /** The picture above the story, rendered on the server. Heads the story tab and never reaches `printView`. */
  storyCover: React.ReactNode;
  /** The print-only copy of the story, rendered on the server. Null exactly when there is no story (mirrors `storyUnavailable`). */
  printView: React.ReactNode;
  storyUnavailable: string | null;
  questionEntries: QuestionEntry[];
  /** The contest's scoring mode; ICPC changes the questions panel (`questions-panel.tsx`). */
  scoring?: Scoring;
  /** Minutes added for a wrong attempt on a question later solved. Read only while `scoring` is `icpc`. */
  icpcPenaltyMin?: number;
  /** The game's shape, or null in a contest that hides it — see SchemaPanel. */
  schema: GameSchema | null;
  initialLog: { items: QueryLogEntry[]; total: number; failed: boolean };
  /** The participant's notes and SQL tabs, or null when the read failed; the notes then say they could not be loaded. */
  workspace: WorkspaceSnapshot | null;
  locale: Locale;
  dict: PlayDictionary;
}) {
  const t = dict.participant.play.workspace;

  // Reports leaving the page and pasting to the organiser (use-signals.ts).
  // The workspace exists only while the contest runs for this participant.
  useSignals(contestId);

  // Sweep the previous user's drafts so their text does not linger on a
  // shared machine.
  useEffect(() => {
    if (accountId !== null) purgeForeignDrafts(accountId);
  }, [accountId]);

  // The latest run, mirrored out of ConsoleEditor so ResultPanel in another
  // subtree can show it; the editor's form never moves, so a run does not
  // remount it.
  const [lastResult, setLastResult] = useState<ConsoleState>({ kind: "idle" });
  // The SQL tab that result came from: the participant may type in another
  // tab while reading it (SPEC.md §5).
  const [resultFrom, setResultFrom] = useState<string | null>(null);
  // Controlled so a completed run can switch to "Result" by itself.
  const [bottomTab, setBottomTab] = useState("result");
  const { containerRef, sizes: widths, commit } = usePaneWidths(contestId);
  // The console column's split is its own group: it is stored in percent,
  // the widths in rem.
  const { containerRef: columnRef, sizes: rows, commit: commitRows } = useConsoleRows(contestId);

  // Collapsed panes are not rendered, divider included, so the grid tracks
  // below are computed: a track for an absent pane would be empty screen.
  const { collapsed, toggle, expand } = usePanelVisibility();
  // The header sits outside the Suspense boundary and cannot see the schema;
  // this tells it whether to offer a schema toggle.
  useSchemaPanel(schema !== null);

  const showSchema = schema !== null && !collapsed.schema;
  const showSide = !collapsed.side;
  const showBottom = !collapsed.bottom;

  // From `narrow` (>=760px) the schema spans its own row underneath, so only
  // the side panel has a column here; from `wide` (>=1024px) all three do.
  const narrowColumns = `minmax(0,1fr)${showSide ? " 1px var(--pane-side)" : ""}`;
  const wideColumns = `${showSchema ? "var(--pane-schema) 1px " : ""}${narrowColumns}`;
  const consoleRows = showBottom ? "minmax(0,var(--pane-editor)) auto minmax(0,1fr)" : "minmax(0,1fr)";

  // The same shortcuts inside the editor's keymap: CodeMirror sees the keydown
  // first, and an unclaimed Ctrl+B in a contenteditable is the browser's
  // "bold". A binding that runs marks the key handled, so the window listener
  // in `panel-toggles.tsx` does not toggle a second time.
  const editorShortcuts = useMemo(
    () => [
      {
        key: "Mod-b",
        // Claimed even without a schema, to keep the browser's "bold" away; it
        // must not then flip a flag nothing on screen reflects.
        run: () => {
          if (schema !== null) toggle("schema");
        },
      },
      { key: "Mod-Alt-b", run: () => toggle("side") },
      { key: "Mod-j", run: () => toggle("bottom") },
    ],
    [schema, toggle],
  );

  /** Brings the schema panel back for ⌘K, which focuses its search field. */
  const revealSchema = useCallback(() => expand("schema"), [expand]);

  /** Shows a bottom tab, expanding the panel if it was collapsed. */
  const showBottomTab = (tab: string) => {
    setBottomTab(tab);
    expand("bottom");
  };

  return (
    <>
      {/* Print-only: hidden on screen, shown by `print:block`. */}
      <div className="hidden print:block">{printView}</div>
      {/* From `narrow` (>=760px) the screen is one viewport tall with no page
          scroll; below it the sections stack and the page scrolls, but each panel
          keeps its own scroll box (see the bottom pane). The height arithmetic
          lives on `page.tsx`'s shell; `narrow:flex-1` takes the rest of it only
          from the breakpoint up, since `flex-1` in a content-height column
          collapses. */}
      <div className="flex min-h-0 flex-col print:hidden narrow:flex-1">
        {/* Schema left, editor and result in the middle, story and questions
          right. The side columns have fixed widths and the middle takes the rest.
          Without a schema its column is dropped rather than left empty. */}
        <div
          ref={containerRef}
          style={
            {
              "--pane-schema": `${widths.schema}rem`,
              "--pane-side": `${widths.side}rem`,
              "--cols-narrow": narrowColumns,
              "--cols-wide": wideColumns,
            } as React.CSSProperties
          }
          className={cn(
            "grid min-h-0 grid-cols-1 narrow:flex-1",
            // The hairlines between panes are grid columns of their own, so dragging
            // changes a width, not a margin. Three panes only from `wide`: at 768px a
            // third pane leaves the result table about 300px. The templates are
            // computed above because collapsed panes drop their tracks; the class
            // names stay literal so Tailwind can read them.
            "narrow:grid-cols-[var(--cols-narrow)] wide:grid-cols-[var(--cols-wide)]",
          )}
        >
          {schema !== null ? (
            // Source order follows the wide layout so tab order matches what is seen;
            // below `narrow` the schema moves after the console.
            //
            // A flex column because the panel claims the cell with `flex-1`, which is
            // inert in a block parent.
            //
            // Every pane in the row declares an `order`, hairlines included: grid
            // auto-placement puts a child with the default `order: 0` before all the
            // numbered ones, which pushes the console into a 1px divider track. Below
            // `narrow` the schema sits between the console and the questions (order 2);
            // from `narrow` to `wide` it spans a second row and must be placed last
            // (order 4).
            <div
              data-panel="schema"
              hidden={!showSchema}
              className={cn(
                "min-h-0 border-line max-wide:col-span-full max-wide:max-h-80 max-wide:border-t max-narrow:order-2 narrow:max-wide:order-4",
                // Conditional rather than fighting the `hidden` attribute; see the bottom
                // pane.
                showSchema && "flex flex-col",
              )}
            >
              <MemoSchemaPanel
                schema={schema}
                hidden={!showSchema}
                onReveal={revealSchema}
                dict={dict}
              />
            </div>
          ) : null}
          {showSchema ? (
            <PaneHandle
              label={t.panes.schema}
              property="--pane-schema"
              value={widths.schema}
              bounds={WIDTH_BOUNDS}
              direction={1}
              containerRef={containerRef}
              onResize={(rem) => commit({ ...widths, schema: rem })}
              className="max-wide:hidden"
            />
          ) : null}
          {/* The console column: the editor on top, the result and log below. The
            split starts at 55/45 and is the participant's to change (SPEC.md §5). On the
            narrow fallback each pane gets a minimum instead and the edge is not
            draggable: a share of a content-height column means nothing. */}
          {/* `grid-cols-1` is load-bearing: an implicit `auto` track grows to the
            result table's max-content width and overflows the page;
            `minmax(0, 1fr)` keeps the sideways scroll inside the table. */}
          <div
            ref={columnRef}
            style={{ "--pane-editor": `${rows.editor}%`, "--console-rows": consoleRows } as React.CSSProperties}
            className="grid min-h-0 grid-cols-1 grid-rows-[var(--console-rows)] border-line max-wide:order-1 max-narrow:grid-rows-none max-narrow:border-b"
          >
            {/* A flex column: the form claims the cell with flex-1, which is inert in
              a block parent. */}
            <div className="flex min-h-0 flex-col max-narrow:min-h-80 max-narrow:border-b max-narrow:border-line">
              <ConsoleEditor
                accountId={accountId}
                contestId={contestId}
                dict={dict}
                shortcuts={editorShortcuts}
                // Stable across renders, so the memoised panels are not disturbed.
                tabs={workspace?.tabs ?? null}
                actions={
                  // The log is a toolbar button, not a tab strip over the result: the
                  // result is what this pane is for.
                  <>
                    <ToolbarButton
                      active={showBottom && bottomTab === "result"}
                      onClick={() => showBottomTab("result")}
                    >
                      {t.tabs.result}
                    </ToolbarButton>
                    <ToolbarButton
                      active={showBottom && bottomTab === "log"}
                      onClick={() => showBottomTab("log")}
                    >
                      {t.tabs.log}
                    </ToolbarButton>
                  </>
                }
                onResult={(state, source) => {
                  setLastResult(state);
                  // The log is not refreshed here: its reads share Run's per-minute
                  // budget, so QueryLogPanel refreshes only when it is shown.
                  if (state.kind !== "idle") {
                    // Set for every run, even one with no tab name, so the heading never
                    // names an earlier run's tab.
                    setResultFrom(source?.tabTitle ?? null);
                    // Open the panel if collapsed: seeing the result is the point of
                    // running (SPEC.md §5).
                    showBottomTab("result");
                  }
                }}
              />
            </div>

            {/* The hairline is the handle. Hidden below `narrow`, where the panes
              carry their own minimum and maximum heights. */}
            {showBottom ? (
              <PaneHandle
                label={t.panes.editor}
                property="--pane-editor"
                value={rows.editor}
                axis="y"
                unit="%"
                bounds={SHARE_BOUNDS}
                direction={1}
                containerRef={columnRef}
                onResize={(share) => commitRows({ editor: share })}
                className="max-narrow:hidden"
              />
            ) : null}

            {/* Both panes stay mounted so switching tabs keeps the result and the
              log's scroll position; they hide by class because `display:flex` would
              beat the `hidden` attribute.

              `max-narrow:max-h-[60svh]` keeps the result a scroll box on the narrow
              fallback; unbounded, a large result pushes the questions thousands of
              pixels down. `svh`, not `dvh`, so a phone's URL bar does not resize the
              box mid-scroll.

              Collapsed, the pane is hidden, not unmounted: `display: none` drops its
              grid track but keeps the log's loaded pages. The display class is
              conditional because any author `display` beats the browser's
              `[hidden] { display: none }`. */}
            <div
              data-panel="bottom"
              hidden={!showBottom}
              className={cn("min-h-0", showBottom && "flex flex-col max-narrow:max-h-[60svh]")}
            >
              <div
                className={cn(
                  "min-h-0 flex-1 overflow-hidden",
                  bottomTab === "result" ? "flex flex-col" : "hidden",
                )}
              >
                <MemoResultPanel
                  contestId={contestId}
                  state={lastResult}
                  sourceTitle={resultFrom}
                  dict={dict}
                />
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
                  // Expanding a collapsed panel counts as being shown, so the log
                  // refreshes; its three-second gate
                  // (`QUERY_LOG_REFRESH_MIN_INTERVAL_MS`) bounds the cost.
                  active={showBottom && bottomTab === "log"}
                  locale={locale}
                  dict={dict}
                />
              </div>
            </div>
          </div>

          {showSide ? (
            <PaneHandle
              label={t.panes.side}
              property="--pane-side"
              value={widths.side}
              bounds={WIDTH_BOUNDS}
              direction={-1}
              containerRef={containerRef}
              onResize={(rem) => commit({ ...widths, side: rem })}
              // Ordered like every pane in the grid; see the schema pane's comment.
              className="max-narrow:hidden max-wide:order-2"
            />
          ) : null}

          {/* The story and questions, stacked below the console on narrow screens.
            Hidden rather than unmounted when collapsed: an answer field holds
            unsaved state, and on Windows AltGr arrives as Ctrl+Alt, so a stray
            keystroke could otherwise discard a typed answer. */}
          <div
            data-panel="side"
            hidden={!showSide}
            className="min-h-0 max-wide:order-3 max-narrow:min-h-100 max-narrow:border-t max-narrow:border-line"
          >
            <MemoSidePanel
              storyBody={storyBody}
              storyCover={storyCover}
              storyUnavailable={storyUnavailable}
              accountId={accountId}
              contestId={contestId}
              questionEntries={questionEntries}
              // Stable across renders, so the memoised panel is not disturbed.
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
 * A quiet toolbar button, marked with a wash rather than an underline so the
 * row reads as controls, not a second navigation.
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
