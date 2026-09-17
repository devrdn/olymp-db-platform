"use client";

import { useActionState, useEffect, useId, useLayoutEffect, useRef, useState } from "react";

import { CodeEditor, type CodeEditorHandle } from "@/components/product/code-editor";
import { buttonVariants } from "@/components/ui/button";
import type { WorkspaceTab } from "@/lib/api/workspace";
import type { PlayDictionary } from "./dictionary";
import { cn } from "@/lib/utils";

import { runQueryAction, type ConsoleState } from "./actions";
import { SqlTabStatus, SqlTabStrip } from "./sql-tabs";
import { LOCAL_TAB_ID, useSqlTabs } from "./use-sql-tabs";

/**
 * The SQL editor — the thing a participant types in, always visible, never
 * behind a tab (Task 3's own requirement).
 *
 * This is deliberately just the input: CodeMirror, the run button, the hint.
 * What a run produced — a table, a row count, a refusal — is not rendered
 * here at all; it goes to `onResult`, and `ResultPanel` (in the "Result" tab
 * of the panel below) is what shows it. Two VS Code habits follow from
 * splitting it this way: the editor's own DOM never changes shape when a
 * query answers (nothing to remount, no risk of losing scroll position or
 * the caret), and a build's own output belongs in a panel, not stitched
 * under the code that produced it.
 *
 * The button is disabled while a query is in flight, and that is not polish:
 * a participant may have one query running at a time, so a second press earns
 * them a refusal they did nothing to deserve and cannot interpret.
 *
 * # The tabs
 *
 * Above the editor is a strip of tabs, one document each, saved on the
 * server as the participant types (§5 of the workspace design). This
 * component is where the three parts meet: `use-sql-tabs.ts` holds what the
 * tabs are and saves them, `sql-tabs.tsx` draws the strip, and `CodeEditor`
 * shows whichever document the strip says is open.
 *
 * Which of them a run uses is the whole point of the arrangement: the hidden
 * `sql` field below always carries the open tab's text, so "Run" and ⌘↵ send
 * what is on screen — and, because a result outlives the tab it came from,
 * the title of that tab goes out with the result through `onResult`.
 */
export function ConsoleEditor({
  contestId,
  dict,
  tabs: initialTabs,
  onResult,
  actions,
}: {
  contestId: string;
  dict: PlayDictionary;
  /**
   * The participant's SQL tabs as the page read them, or null when that read
   * failed — the editor then works on one tab of its own and says that
   * nothing here is saved, the way the notes field does.
   */
  tabs: WorkspaceTab[] | null;
  /**
   * Controls the surrounding screen wants at the right end of the console's
   * toolbar — the query log and the CSV download. They belong to the
   * workspace, not to this form, and passing them in is what keeps this
   * component about one thing: the query, and running it.
   */
  actions?: React.ReactNode;
  /**
   * Called once per completed run — including a refusal — never while one is
   * still in flight. `useActionState`'s own `state` only changes value when
   * the action settles (it holds steady, and only `running` moves, while
   * pending), so this effect fires exactly once per run rather than once per
   * render.
   */
  onResult: (state: ConsoleState, source?: RunSource) => void;
}) {
  const t = dict.participant.console;
  const te = dict.participant.play.workspace.editor;
  const [state, run, running] = useActionState<ConsoleState, FormData>(runQueryAction, {
    kind: "idle",
  });
  /**
   * The name of the tab the running query was started from — read as the run
   * starts, because a participant reading an answer often goes on typing in
   * another tab, and the result belongs to the tab it was run from.
   */
  const [runFrom, setRunFrom] = useState<string | null>(null);

  useEffect(() => {
    onResult(state, runFrom === null ? undefined : { tabTitle: runFrom });
    // onResult is an inline closure the workspace passes down, recreated
    // every one of its own renders — not actually stable, whatever an
    // earlier version of this comment claimed (finding 7). It does not need
    // to be: state is the one dependency this effect actually reacts to,
    // and onResult is read fresh from the closure each time this effect
    // runs, which is exactly once per completed run either way.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [state]);

  // CodeMirror owns the visible query text from the moment it mounts, and a
  // native `<textarea>`/`<input>` is the only thing `requestFormReset` (React
  // 19 calls it on this form the instant a run starts, before the action even
  // settles) can reach — CodeMirror's contentEditable div is not a form
  // control, so the reset that used to wipe a participant's query on a
  // refusal (finding 2) cannot touch it at all. What the reset *does* still
  // reach is `mirror` below: the hidden, visually-suppressed textarea that is
  // the actual `name="sql"` field the browser's own FormData is built from at
  // submit time. Losing sync there has no visible consequence — the
  // participant never sees this node — but it would silently turn the next
  // "Run" click (with nothing retyped since the last one) into a query for
  // the empty string, so it gets the same imperative restore finding 2's own
  // textarea used to need, just aimed at a field nobody looks at instead of
  // the one everybody does.
  const formRef = useRef<HTMLFormElement>(null);
  const mirrorRef = useRef<HTMLTextAreaElement>(null);
  const lastTyped = useRef("");
  const editorRef = useRef<CodeEditorHandle>(null);
  /**
   * The tab the field below holds the text of — the callbacks that run
   * outside a render read it, and the effect that keeps the field in step
   * compares against it.
   *
   * Seeded with the tab the field is rendered from, not with null. A page
   * that was server-rendered opens on that same tab during hydration, so the
   * effect still skips its first run and leaves a browser-restored value
   * alone; a client-side navigation (the ordinary way onto this screen, from
   * /my) renders the remembered tab straight away, and there the first run
   * is exactly what puts that tab's text where a run reads it.
   */
  const openRef = useRef<string>(initialTabs?.[0]?.id ?? LOCAL_TAB_ID);
  const panelId = useId();
  const tabPrefix = useId();

  const tabs = useSqlTabs({
    contestId,
    initial: initialTabs,
    localTitle: te.local,
    confirmClose: (title) => window.confirm(te.closeConfirm.replace("{tab}", title)),
    // A draft that beat the server's copy: it belongs in that tab's
    // document, and — when it is the tab on screen — in the field a run is
    // built from.
    onRestore: (id, text) => {
      editorRef.current?.setDocumentValue(id, text);
      if (id !== openRef.current) return;
      lastTyped.current = text;
      if (mirrorRef.current) mirrorRef.current.value = text;
    },
    onDrop: (id) => editorRef.current?.dropDocument(id),
  });

  // What a run sends, kept in step with the tab that is open. It does
  // nothing while the field already holds that tab's text, which is the
  // server-rendered case — where what the field holds may be a value the
  // browser restored across a soft reload, and not this effect's to
  // overwrite.
  const { activeId, textOf } = tabs;
  useLayoutEffect(() => {
    if (openRef.current === activeId) return;
    openRef.current = activeId;
    const text = textOf(activeId);
    lastTyped.current = text;
    if (mirrorRef.current) mirrorRef.current.value = text;
  }, [activeId, textOf]);

  useLayoutEffect(() => {
    const el = mirrorRef.current;
    // Guard against the empty ref on mount: `lastTyped` starts at `""`
    // because CodeEditor has not reported a change yet, but the mirror's own
    // `.value` may already hold something real — a browser-restored form
    // value across a soft reload, or a server-rendered value React's
    // hydration reused. Restoring blindly here would erase that value the
    // instant this effect first runs, which is the loss of work this effect
    // exists to prevent. When `lastTyped` is genuinely empty (untouched, or
    // the participant deliberately cleared the field), the native reset's own
    // target value is also `""`, so skipping the write here costs nothing.
    if (el && lastTyped.current !== "" && el.value !== lastTyped.current) {
      el.value = lastTyped.current;
    }
  });

  return (
    <form
      ref={formRef}
      action={run}
      // Read as the run starts rather than when it settles: a participant
      // reading an answer often goes on typing in another tab, and the
      // result belongs to the tab it was run from. The submit event is where
      // "now" is — React calls this before the action itself, and ⌘↵ inside
      // the editor arrives here too (`requestSubmit`).
      onSubmit={() => {
        setRunFrom(tabs.tabs.find((tab) => tab.id === tabs.activeId)?.title ?? "");
      }}
      className="flex min-h-0 flex-1 flex-col"
    >
      <input type="hidden" name="contestId" value={contestId} />
      {/*
       * The real form field: what the browser restores across a soft reload
       * (the same mechanism the plain-textarea implementation relied on,
       * still a native textarea here for exactly that reason) and what
       * FormData reads at submit time. `sr-only` hides it visually without
       * `display:none` — kept a normal, laid-out node, because the
       * restoration this depends on is a browser behaviour tied to a form
       * control existing in the DOM, not to it being visible. `aria-hidden`
       * plus a negative `tabIndex` keep it out of the accessibility tree and
       * the tab order; CodeEditor below carries the same `t.label` as its own
       * `aria-label`, so nothing is announced twice and nothing is announced
       * zero times.
       */}
      <textarea
        ref={mirrorRef}
        name="sql"
        // The open tab's text. On the server, and so during hydration, that
        // is the first tab — storage has not been read yet; on a client-side
        // navigation it is the remembered one from the first render, which
        // is what makes a run send the text on screen rather than the first
        // tab's (the effect above catches the hydrating case).
        defaultValue={textOf(activeId)}
        aria-hidden="true"
        tabIndex={-1}
        className="sr-only"
      />
      {/* The toolbar the design puts above the editor, not below it
          (docs/design/preview.html, "SQL-консоль"): the action a participant
          reaches for most is at the top of the pane, where it does not move
          when the result underneath changes height. `actions` is whatever the
          screen around this console wants beside it — the query log and the
          CSV download are the workspace's, not the form's. */}
      <div className="flex shrink-0 items-center gap-2 border-b border-line px-3 py-2">
        <button
          type="submit"
          disabled={running}
          className={cn(buttonVariants({ variant: "primary", size: "sm" }))}
        >
          {running ? t.running : t.run}
          {/* Decorative: the shortcut is bound in the editor, and reading
              "command return" after every button label is noise. */}
          <span aria-hidden="true" className="ml-1.5 font-mono text-label opacity-60">
            ⌘↵
          </span>
        </button>
        {/* The one rule a participant can break by typing — a second
            statement is refused — so it stays in sight rather than behind
            a "?". It takes the toolbar's spare width and may wrap there on a
            phone rather than push the buttons off the edge. */}
        <span className="min-w-0 flex-1 text-small text-ink-3">{t.hint}</span>
        {actions}
      </div>

      <SqlTabStrip
        tabs={tabs.tabs}
        activeId={tabs.activeId}
        idPrefix={tabPrefix}
        panelId={panelId}
        closed={tabs.closed !== null}
        dict={dict}
        status={
          <SqlTabStatus
            engine={tabs.activeEngine}
            error={tabs.error}
            stored={tabs.stored}
            dict={dict}
          />
        }
        onSelect={tabs.select}
        onCreate={tabs.create}
        onRename={tabs.rename}
        onClose={tabs.close}
        onMove={tabs.move}
      />

      <div
        id={panelId}
        role="tabpanel"
        aria-labelledby={`${tabPrefix}${tabs.activeId}`}
        className="flex min-h-0 flex-1 flex-col"
      >
        <CodeEditor
          ref={editorRef}
          className="min-h-0 flex-1"
          onSubmit={() => formRef.current?.requestSubmit()}
          ariaLabel={t.label}
          placeholder={t.placeholder}
          documentId={tabs.activeId}
          getDocumentValue={tabs.textOf}
          getInitialValue={() => mirrorRef.current?.value ?? ""}
          onChange={(text) => {
            lastTyped.current = text;
            if (mirrorRef.current) mirrorRef.current.value = text;
            // Outside React's own data flow on purpose: this is the typing
            // path, and it must not render anything (CodeEditor's contract).
            tabs.edited(openRef.current ?? tabs.activeId, text);
          }}
          errorPosition={state.kind === "refused" ? state.position : undefined}
          // A fresh `state` object every settled run, even a refusal at the
          // exact same character as the one before — see CodeEditor's own
          // doc comment on `errorToken` for why that identity, not just the
          // position number, is what the underline has to key on
          // (finding 4).
          errorToken={state}
        />
      </div>
    </form>
  );
}

/** Which tab a completed run was started from, for the result's own heading. */
export type RunSource = { tabTitle: string };
