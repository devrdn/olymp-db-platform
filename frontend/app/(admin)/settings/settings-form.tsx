"use client";

import { useActionState, useState } from "react";

import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { feedbackHref, type Settings } from "@/lib/api/settings";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { saveSettingsAction, type SettingsState } from "./actions";
import { messageForCode } from "@/lib/i18n/errors";

/**
 * The installation's details in one form. Colours are not a setting: they are
 * tokens with guaranteed contrast (SPEC 3.3).
 */
export function SettingsForm({ settings, dict }: { settings: Settings; dict: Dictionary }) {
  const t = dict.settings;
  const [state, formAction, pending] = useActionState<SettingsState, FormData>(
    saveSettingsAction,
    {},
  );
  // Checked here as well as in the action, so the message sits under the field
  // it is about; the API's refusal of the same field goes there too.
  const [feedbackInvalid, setFeedbackInvalid] = useState(false);
  const feedbackRefused = feedbackInvalid || state.code === "invalid_feedback_url";

  const failure =
    state.code && state.code !== "invalid_feedback_url" ? messageForCode(state.code, dict.errors) : null;

  return (
    // Keyed on the server's values, so a save replaces the draft.
    <form
      key={settings.name + settings.contact + settings.feedback}
      action={formAction}
      onSubmit={(event) => {
        const feedback = String(new FormData(event.currentTarget).get("feedback") ?? "").trim();
        const invalid = feedback !== "" && feedbackHref(feedback) === null;
        setFeedbackInvalid(invalid);
        if (invalid) event.preventDefault();
      }}
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

      <Field
        id="feedback"
        label={t.feedback}
        hint={t.feedbackHint}
        error={feedbackRefused ? dict.errors.invalid_feedback_url : undefined}
        help={t.feedbackHelp}
        helpLabel={dict.chrome.helpLabel}
      >
        {/* Not `type="url"`: the browser would answer "forms.gle/x" with its own
            message, which says nothing about https. */}
        <Input name="feedback" inputMode="url" spellCheck={false} defaultValue={settings.feedback} />
      </Field>

      <div className="flex flex-wrap items-center gap-4">
        <Button type="submit" disabled={pending}>
          {pending ? t.saving : t.save}
        </Button>

        {/* `status`, not `alert`: a successful save is not an interruption. */}
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
