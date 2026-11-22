"use client";

import { useActionState } from "react";

import { Button } from "@/components/ui/button";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { messageForCode } from "@/lib/i18n/errors";
import { useHydrated } from "@/lib/use-hydrated";

import { type DeleteDraftState, deleteDraftAction } from "./actions";
import { Panel } from "./settings-panels";

/**
 * Deleting a draft, last on the page and away from the status buttons. The
 * question is asked in the submit handler, which React skips when it replays a
 * submission queued before hydration, so the button is enabled only once the
 * page is hydrated: before that, and without JavaScript, it cannot be pressed.
 */
export function DeleteDraftPanel({
  contestId,
  title,
  dict,
}: {
  contestId: string;
  title: string;
  dict: Dictionary;
}) {
  const t = dict.workspace.settings.deleteDraft;
  const [state, formAction, pending] = useActionState<DeleteDraftState, FormData>(deleteDraftAction, {});
  const hydrated = useHydrated();

  return (
    <form
      action={formAction}
      className="contents"
      onSubmit={(event) => {
        if (!window.confirm(t.confirm.replace("{title}", title))) event.preventDefault();
      }}
    >
      <Panel title={t.heading} dict={dict}>
        <input type="hidden" name="contestId" value={contestId} />

        <p className="max-w-body text-body text-ink-2">{t.body}</p>

        <div className="flex flex-wrap items-center gap-4">
          <Button type="submit" variant="danger" disabled={!hydrated || pending}>
            {t.action}
          </Button>

          {state.code ? (
            <p role="alert" className="max-w-body text-small text-bad">
              {messageForCode(state.code, dict.errors)}
            </p>
          ) : null}
        </div>
      </Panel>
    </form>
  );
}
