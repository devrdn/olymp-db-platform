"use client";

import { useActionState, useEffect, useLayoutEffect, useRef } from "react";

import { buttonVariants } from "@/components/ui/button";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { runQueryAction, type ConsoleState } from "./actions";

/**
 * The SQL editor — the thing a participant types in, always visible, never
 * behind a tab (Task 3's own requirement).
 *
 * This is deliberately just the input: the textarea, the run button, the
 * hint. What a run produced — a table, a row count, a refusal — is not
 * rendered here at all; it goes to `onResult`, and `ResultPanel` (in the
 * "Result" tab of the panel below) is what shows it. Two VS Code habits
 * follow from splitting it this way: the editor's own DOM never changes
 * shape when a query answers (nothing to remount, no risk of losing the
 * textarea's scroll position or the caret), and a build's own output belongs
 * in a panel, not stitched under the code that produced it.
 *
 * The button is disabled while a query is in flight, and that is not polish:
 * a participant may have one query running at a time, so a second press earns
 * them a refusal they did nothing to deserve and cannot interpret.
 */
export function ConsoleEditor({
  contestId,
  dict,
  onResult,
}: {
  contestId: string;
  dict: Dictionary;
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

  // Finding 2: this textarea is deliberately uncontrolled (no `value` prop,
  // no `onChange`) — that is what keeps every keystroke from re-rendering
  // ResultPanel and the rest of this tree, and it is a property this fix
  // must not give up. But React 19 calls `requestFormReset` on this form the
  // instant a run starts (before the action even settles), and the native
  // reset algorithm sets an uncontrolled field's `.value` back to its
  // `.defaultValue`. A syntax error — or any refusal at all — would silently
  // erase the query a participant was mid-debugging, for the whole two
  // hours.
  //
  // Passing a `defaultValue` prop that tracks the latest keystroke (tried
  // first) is not allowed either, for a narrower reason: that means reading
  // a ref's `.current` during render to compute the prop, and the
  // `react-hooks/refs` rule refuses that outright — refs may only be read in
  // an event handler or an effect, never in the render body itself, because
  // a render can in principle run without ever committing.
  //
  // So the restore happens imperatively, after the fact: `lastTyped` is a
  // ref, updated by `onInput` (fired on every keystroke — chosen only
  // because it is not the *controlled*-input `onChange` contract) — a ref
  // write schedules no render, so the typing path stays exactly as free of
  // renders as it already was. `useLayoutEffect` runs after every commit,
  // synchronously before the browser paints — which is to say, after
  // `requestFormReset`'s own DOM mutation has already run (both are part of
  // the same commit, and the reset happens during the earlier mutation
  // phase) but before anything is drawn. If the field's `.value` no longer
  // matches what was actually typed, this puts it back before there is
  // anything to see — never a visible flash of an emptied field.
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const lastTyped = useRef("");

  useLayoutEffect(() => {
    const el = textareaRef.current;
    if (el && el.value !== lastTyped.current) {
      el.value = lastTyped.current;
    }
  });

  return (
    <form action={run} className="flex min-h-0 flex-1 flex-col gap-3 p-4">
      <input type="hidden" name="contestId" value={contestId} />
      <label className="flex min-h-0 flex-1 flex-col gap-2">
        <span className="sr-only">{t.label}</span>
        <textarea
          ref={textareaRef}
          name="sql"
          spellCheck={false}
          placeholder={t.placeholder}
          onInput={(event) => {
            lastTyped.current = event.currentTarget.value;
          }}
          className="min-h-0 w-full flex-1 resize-none border border-edge bg-bg p-3 font-mono text-body text-ink outline-none focus-visible:border-accent"
        />
      </label>
      <div className="flex shrink-0 items-center gap-3">
        <button type="submit" disabled={running} className={cn(buttonVariants({ variant: "primary" }))}>
          {running ? t.running : t.run}
        </button>
        <span className="text-small text-ink-2">{t.hint}</span>
      </div>
    </form>
  );
}
