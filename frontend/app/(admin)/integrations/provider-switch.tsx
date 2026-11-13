"use client";

import { useActionState } from "react";

import { Button } from "@/components/ui/button";
import type { ProviderId } from "@/lib/auth/external";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { messageForCode } from "@/lib/i18n/errors";

import { switchProviderAction, type SwitchState } from "./actions";

/**
 * The button that turns a configured provider off, or back on. `labelledBy` is the id of the
 * provider's heading, which names the form: every provider's button reads the same.
 */
export function ProviderSwitch({
  provider,
  enabled,
  labelledBy,
  dict,
}: {
  provider: ProviderId;
  enabled: boolean;
  labelledBy: string;
  dict: Dictionary;
}) {
  const t = dict.settings.integrations;
  const [state, submit, pending] = useActionState<SwitchState, FormData>(switchProviderAction, {});
  const failure = state.code ? messageForCode(state.code, dict.errors) : null;

  return (
    <form action={submit} aria-labelledby={labelledBy} className="flex flex-col gap-2">
      <input type="hidden" name="provider" value={provider} />
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
  );
}
