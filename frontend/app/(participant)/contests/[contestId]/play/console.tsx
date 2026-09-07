"use client";

import { useActionState, useEffect } from "react";

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
    // onResult is the workspace's own setState, stable across renders; state
    // is the one dependency this effect actually reacts to.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [state]);

  return (
    <form action={run} className="flex min-h-0 flex-1 flex-col gap-3 p-4">
      <input type="hidden" name="contestId" value={contestId} />
      <label className="flex min-h-0 flex-1 flex-col gap-2">
        <span className="sr-only">{t.label}</span>
        <textarea
          name="sql"
          spellCheck={false}
          placeholder={t.placeholder}
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
