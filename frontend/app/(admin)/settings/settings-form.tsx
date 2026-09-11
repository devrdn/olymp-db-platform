"use client";

import { useActionState } from "react";

import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import type { Settings } from "@/lib/api/settings";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { saveSettingsAction, type SettingsState } from "./actions";

/**
 * The installation's own details.
 *
 * One form and one save, because these are one thing: a name and the address
 * to write to when it goes wrong are both answers to "whose installation is
 * this". Nothing here is a server setting — the palette in particular is not,
 * because colours are tokens whose contrast the system guarantees and a field
 * for them would be a field for breaking it (SPEC 3.3).
 */
export function SettingsForm({ settings, dict }: { settings: Settings; dict: Dictionary }) {
  const t = dict.settings;
  const [state, formAction, pending] = useActionState<SettingsState, FormData>(
    saveSettingsAction,
    {},
  );

  const failure = state.code
    ? ((dict.errors as Record<string, string>)[state.code] ?? dict.errors.fallback)
    : null;

  return (
    // Keyed on what the server last returned, so a saved value replaces what
    // was typed rather than the field keeping a stale draft.
    <form
      key={settings.name + settings.contact}
      action={formAction}
      className="flex max-w-narrative flex-col gap-6 border-t border-line pt-6"
    >
      <Field id="name" label={t.name} help={t.nameHelp} helpLabel={dict.chrome.helpLabel}>
        <Input name="name" defaultValue={settings.name} required />
      </Field>

      <Field
        id="contact"
        label={t.contact}
        hint={t.contactHint}
        help={t.contactHelp}
        helpLabel={dict.chrome.helpLabel}
      >
        <Input name="contact" type="email" defaultValue={settings.contact} />
      </Field>

      <div className="flex flex-wrap items-center gap-4">
        <Button type="submit" disabled={pending}>
          {pending ? t.saving : t.save}
        </Button>

        {/* `status`, not `alert`: a save that worked is not an interruption. */}
        {state.saved && !failure ? (
          <p role="status" className="text-small text-good">
            {t.saved}
          </p>
        ) : null}

        {failure ? (
          <p role="alert" className="max-w-body text-small text-bad">
            {failure}
          </p>
        ) : null}
      </div>
    </form>
  );
}
