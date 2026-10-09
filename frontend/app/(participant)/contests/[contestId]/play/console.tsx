"use client";

import { useActionState, useEffect, useId, useLayoutEffect, useRef, useState } from "react";

import { CodeEditor, type CodeEditorHandle, type EditorShortcut } from "@/components/product/code-editor";
import { buttonVariants } from "@/components/ui/button";
import type { WorkspaceTab } from "@/lib/api/workspace";
import type { PlayDictionary } from "./dictionary";
import { cn } from "@/lib/utils";

import { runQueryAction, type ConsoleState } from "./actions";
import { SqlTabStatus, SqlTabStrip } from "./sql-tabs";
import { LOCAL_TAB_ID, useSqlTabs } from "./use-sql-tabs";

/**
 * The SQL editor: always visible, never behind a tab. It is only the input
 * (CodeMirror, the run button, the hint); what a run produced goes to
 * `onResult` and `ResultPanel` shows it, so the editor's DOM never changes
 * shape when a query answers.
 *
 * The button is disabled while a query is in flight: a participant may run
 * one query at a time, and a second press would earn an unexplained refusal.
 *
 * Above the editor is a strip of SQL tabs saved as the participant types
 * (SPEC.md §5): `use-sql-tabs.ts` holds and saves them, `sql-tabs.tsx`
 * draws the strip, and `CodeEditor` shows the open document. The hidden
 * `sql` field always carries the open tab's text, so a run sends what is on
 * screen, and the tab's title goes out with the result through `onResult`.
 */
export function ConsoleEditor({
  accountId,
  contestId,
  dict,
  tabs: initialTabs,
  onResult,
  actions,
  shortcuts,
}: {
  /** Whose console this is; each tab's draft is keyed by it. */
  accountId: string | null;
  contestId: string;
  dict: PlayDictionary;
  /**
   * The participant's SQL tabs, or null when the read failed; the editor then
   * works on one local tab and says nothing is saved.
   */
  tabs: WorkspaceTab[] | null;
  /** Controls the workspace puts at the right end of the toolbar. */
  actions?: React.ReactNode;
  /** Keys the workspace owns that the editor must not swallow (the panel toggles). */
  shortcuts?: readonly EditorShortcut[];
  /**
   * Called once per completed run, refusals included, never while one is in
   * flight: `useActionState`'s `state` changes only when the action settles.
   */
  onResult: (state: ConsoleState, source?: RunSource) => void;
}) {
  const t = dict.participant.console;
  const te = dict.participant.play.workspace.editor;
  const [state, run, running] = useActionState<ConsoleState, FormData>(runQueryAction, {
    kind: "idle",
  });
  /**
   * The tab the running query started from, read as the run starts: the
   * participant may go on typing in another tab, and the result belongs to
   * the one it was run from.
   */
  const [runFrom, setRunFrom] = useState<string | null>(null);

  useEffect(() => {
    onResult(state, runFrom === null ? undefined : { tabTitle: runFrom });
    // onResult is a fresh closure on every workspace render; only `state`
    // should trigger this, once per completed run.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [state]);

  // `requestFormReset` (React 19 calls it the instant a run starts) cannot
  // touch CodeMirror, but it does reset `mirror`, the hidden `name="sql"`
  // textarea FormData is built from. Left alone, the next "Run" without
  // retyping would send an empty query, so the mirror is restored below.
  const formRef = useRef<HTMLFormElement>(null);
  const mirrorRef = useRef<HTMLTextAreaElement>(null);
  const lastTyped = useRef("");
  const editorRef = useRef<CodeEditorHandle>(null);
  /**
   * The tab whose text the hidden field holds. Seeded with the tab the field
   * is rendered from: after server rendering hydration opens on that tab, so
   * the sync effect skips its first run and leaves a browser-restored value
   * alone; on a client-side navigation the remembered tab renders at once and
   * the first run puts its text in the field.
   */
  const openRef = useRef<string>(initialTabs?.[0]?.id ?? LOCAL_TAB_ID);
  const panelId = useId();
  const tabPrefix = useId();

  const tabs = useSqlTabs({
    accountId,
    contestId,
    initial: initialTabs,
    localTitle: te.local,
    confirmClose: (title) => window.confirm(te.closeConfirm.replace("{tab}", title)),
    // A draft that beat the server copy goes into that tab's document and, if
    // the tab is open, into the field a run is built from.
    onRestore: (id, text) => {
      editorRef.current?.setDocumentValue(id, text);
      if (id !== openRef.current) return;
      lastTyped.current = text;
      if (mirrorRef.current) mirrorRef.current.value = text;
    },
    onDrop: (id) => editorRef.current?.dropDocument(id),
  });

  // Keeps the field a run sends in step with the open tab. A no-op while it
  // already holds that tab, where its value may be one the browser restored.
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
    // Skipped while `lastTyped` is empty: the mirror may already hold a
    // browser-restored or hydrated value, and restoring "" would erase it.
    // An empty `lastTyped` matches what the reset writes anyway.
    if (el && lastTyped.current !== "" && el.value !== lastTyped.current) {
      el.value = lastTyped.current;
    }
  });

  return (
    <form
      ref={formRef}
      action={run}
      // Read as the run starts, not when it settles (see `runFrom`). React
      // calls this before the action, and ⌘↵ arrives here too (`requestSubmit`).
      onSubmit={() => {
        // Null, not "", when there is no tab: an empty name would render as
        // "From " with nothing after it.
        setRunFrom(tabs.tabs.find((tab) => tab.id === tabs.activeId)?.title ?? null);
      }}
      className="flex min-h-0 flex-1 flex-col"
    >
      <input type="hidden" name="contestId" value={contestId} />
      {/*
       * The real form field: a native textarea so the browser restores it across
       * a soft reload, and what FormData reads. `sr-only` rather than
       * `display:none`, since that restoration needs a laid-out form control.
       * `aria-hidden` and `tabIndex={-1}` keep it out of the accessibility tree
       * and tab order; CodeEditor carries the same label.
       */}
      <textarea
        ref={mirrorRef}
        name="sql"
        // The open tab's text: the first tab on the server and during hydration
        // (storage is unread), the remembered one on a client-side navigation.
        // The effect above covers the hydrating case.
        defaultValue={textOf(activeId)}
        aria-hidden="true"
        tabIndex={-1}
        className="sr-only"
      />
      {/* The toolbar sits above the editor so the action used most does not
          move when the result changes height. */}
      <div className="flex shrink-0 items-center gap-2 border-b border-line px-3 py-2">
        <button
          type="submit"
          disabled={running}
          className={cn(buttonVariants({ variant: "primary", size: "sm" }))}
        >
          {running ? t.running : t.run}
          {/* Decorative: the shortcut is bound in the editor. */}
          <span aria-hidden="true" className="ml-1.5 font-mono text-label opacity-60">
            ⌘↵
          </span>
        </button>
        {/* The one rule typing can break (a second statement is refused), kept in
            sight. It may wrap on a phone rather than push the buttons off. */}
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
        // Pastes are reported to the organiser (use-signals.ts). On the wrapper,
        // because CodeMirror owns the element that takes the text.
        data-paste-target="editor"
      >
        <CodeEditor
          ref={editorRef}
          className="min-h-0 flex-1"
          onSubmit={() => formRef.current?.requestSubmit()}
          shortcuts={shortcuts}
          ariaLabel={t.label}
          placeholder={t.placeholder}
          documentId={tabs.activeId}
          getDocumentValue={tabs.textOf}
          getInitialValue={() => mirrorRef.current?.value ?? ""}
          onChange={(text) => {
            lastTyped.current = text;
            if (mirrorRef.current) mirrorRef.current.value = text;
            // Outside React's data flow: the typing path must not render
            // (CodeEditor's contract).
            tabs.edited(openRef.current ?? tabs.activeId, text);
          }}
          errorPosition={state.kind === "refused" ? state.position : undefined}
          // A fresh object every settled run, so a refusal at the same position
          // still re-keys the underline (see `errorToken` in CodeEditor).
          errorToken={state}
        />
      </div>
    </form>
  );
}

/** The tab a completed run was started from, for the result's heading. */
export type RunSource = { tabTitle: string };
