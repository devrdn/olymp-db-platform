"use client";

import { useActionState } from "react";

import { Button } from "@/components/ui/button";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { messageForCode } from "@/lib/i18n/errors";

import { switchArchiveAction, type SwitchState } from "./actions";

/** The contest archive's state and the button that turns it off, or back on. */
export function ArchiveSwitch({ enabled, dict }: { enabled: boolean; dict: Dictionary }) {
  const t = dict.settings.archive;
  const [state, submit, pending] = useActionState<SwitchState, FormData>(switchArchiveAction, {});
  const failure = state.code ? messageForCode(state.code, dict.errors) : null;

  return (
    <section aria-labelledby="archive-heading" className="flex max-w-narrative flex-col gap-4 border-t border-line pt-6">
      <div className="flex flex-col gap-1.5">
        <div className="flex flex-wrap items-center gap-3">
          <h2 id="archive-heading" className="text-h3 text-ink">
            {t.heading}
          </h2>
          <span className="text-small text-ink-3">{enabled ? t.on : t.off}</span>
        </div>
        <p className="max-w-body text-body text-ink-2">{t.help}</p>
      </div>

      <form action={submit} className="flex flex-col gap-2">
        <input type="hidden" name="enabled" value={String(!enabled)} />
        <div>
          <Button type="submit" variant="secondary" disabled={pending}>
            {enabled ? t.switchOff : t.switchOn}
          </Button>
        </div>
        {failure ? (
          <p role="alert" className="max-w-body text-small text-bad">
            {failure}
          </p>
        ) : null}
      </form>
    </section>
  );
}
