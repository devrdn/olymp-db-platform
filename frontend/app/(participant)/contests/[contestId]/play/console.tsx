"use client";

import { useActionState, useEffect, useLayoutEffect, useRef } from "react";

import { CodeEditor } from "@/components/product/code-editor";
import { buttonVariants } from "@/components/ui/button";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { runQueryAction, type ConsoleState } from "./actions";

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
 */
export function ConsoleEditor({
  contestId,
  dict,
  onResult,
  actions,
}: {
  contestId: string;
  dict: Dictionary;
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
  onResult: (state: ConsoleState) => void;
}) {
  const t = dict.participant.console;
  const [state, run, running] = useActionState<ConsoleState, FormData>(runQueryAction, {
    kind: "idle",
  });

  useEffect(() => {
    onResult(state);
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
    <form ref={formRef} action={run} className="flex min-h-0 flex-1 flex-col">
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
        defaultValue=""
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
        <div className="flex-1" />
        {actions}
      </div>

      <div className="flex min-h-0 flex-1 flex-col">
        <CodeEditor
          className="min-h-0 flex-1"
          onSubmit={() => formRef.current?.requestSubmit()}
          ariaLabel={t.label}
          placeholder={t.placeholder}
          getInitialValue={() => mirrorRef.current?.value ?? ""}
          onChange={(text) => {
            lastTyped.current = text;
            if (mirrorRef.current) mirrorRef.current.value = text;
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
