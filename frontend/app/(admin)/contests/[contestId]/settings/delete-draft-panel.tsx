"use client";

import { ActionButton, ActionForm, ActionOutcome } from "@/components/product/action-form";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { fill } from "@/lib/i18n/fill";

import { deleteDraftAction } from "./actions";
import { Panel } from "./settings-panels";

/** Deleting a draft, last on the page and away from the status buttons. */
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

  return (
    <ActionForm
      action={deleteDraftAction}
      fields={{ contestId }}
      confirm={{ title: fill(t.confirm, { title }), answer: t.action, cancel: dict.chrome.cancel, danger: true }}
      errors={dict.errors}
      className="contents"
    >
      <Panel title={t.heading} dict={dict}>
        <p className="max-w-body text-body text-ink-2">{t.body}</p>

        <div className="flex flex-wrap items-center gap-4">
          <ActionButton variant="danger">{t.action}</ActionButton>
          <ActionOutcome />
        </div>
      </Panel>
    </ActionForm>
  );
}
