"use client";

import { ActionButton, ActionForm, ActionOutcome } from "@/components/product/action-form";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { switchArchiveAction } from "./actions";

/** The contest archive's state and the button that turns it off, or back on. */
export function ArchiveSwitch({ enabled, dict }: { enabled: boolean; dict: Dictionary }) {
  const t = dict.settings.archive;

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

      <ActionForm
        action={switchArchiveAction}
        fields={{ enabled: String(!enabled) }}
        errors={dict.errors}
        className="flex flex-col gap-2"
      >
        <div>
          <ActionButton variant="secondary">{enabled ? t.switchOff : t.switchOn}</ActionButton>
        </div>
        <ActionOutcome />
      </ActionForm>
    </section>
  );
}
