"use client";

import { ActionButton, ActionForm, ActionOutcome } from "@/components/product/action-form";
import type { ProviderId } from "@/lib/auth/external";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { switchProviderAction } from "./actions";

/**
 * The button that turns a configured provider off, or back on. `describedBy` is the id of the
 * provider's heading, which describes the button: every provider's button reads the same.
 */
export function ProviderSwitch({
  provider,
  enabled,
  describedBy,
  dict,
}: {
  provider: ProviderId;
  enabled: boolean;
  describedBy: string;
  dict: Dictionary;
}) {
  const t = dict.settings.integrations;

  return (
    <ActionForm
      action={switchProviderAction}
      fields={{ provider, enabled: String(!enabled) }}
      errors={dict.errors}
      className="flex flex-col gap-2"
    >
      <div>
        <ActionButton variant="secondary" aria-describedby={describedBy}>
          {enabled ? t.switchOff : t.switchOn}
        </ActionButton>
      </div>
      <ActionOutcome />
    </ActionForm>
  );
}
