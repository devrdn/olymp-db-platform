"use client";

import { useState } from "react";

import { ActionButton, ActionForm, ActionOutcome } from "@/components/product/action-form";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { MAIL_LANGUAGES, feedbackHref, type Settings } from "@/lib/api/settings";
import { LOCALE_NAMES } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { saveSettingsAction } from "./actions";

/**
 * The installation's details in one form. Colours are not a setting: they are
 * tokens with guaranteed contrast (SPEC 3.3).
 */
export function SettingsForm({ settings, dict }: { settings: Settings; dict: Dictionary }) {
  const t = dict.settings;
  // Checked here as well as in the action, so the message sits under the field
  // it is about; the API's refusal of the same field goes there too.
  const [feedbackInvalid, setFeedbackInvalid] = useState(false);

  return (
    // The server's values, so a save replaces the draft.
    <ActionForm
      action={saveSettingsAction}
      errors={dict.errors}
      revision={settings.name + settings.contact + settings.feedback + settings.mailLanguage}
      onSubmit={(event) => {
        const feedback = String(new FormData(event.currentTarget).get("feedback") ?? "").trim();
        const invalid = feedback !== "" && feedbackHref(feedback) === null;
        setFeedbackInvalid(invalid);
        if (invalid) event.preventDefault();
      }}
      className="flex max-w-narrative flex-col gap-6 border-t border-line pt-6"
    >
      {(state) => (
        <>
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
            error={feedbackInvalid || state.code === "invalid_feedback_url" ? dict.errors.invalid_feedback_url : undefined}
            help={t.feedbackHelp}
            helpLabel={dict.chrome.helpLabel}
          >
            {/* Not `type="url"`: the browser would answer "forms.gle/x" with its own
                message, which says nothing about https. */}
            <Input name="feedback" inputMode="url" spellCheck={false} defaultValue={settings.feedback} />
          </Field>

          <Field id="mailLanguage" label={t.mailLanguage} hint={t.mailLanguageHint}>
            <select
              name="mailLanguage"
              defaultValue={settings.mailLanguage}
              className={cn(
                "h-(--control-h) max-w-60 rounded-none border border-edge bg-transparent px-3",
                "text-control text-ink transition-colors duration-(--t-input) ease-standard",
                "hover:border-ink-2 focus-visible:border-ink",
              )}
            >
              {MAIL_LANGUAGES.map((code) => (
                <option key={code} value={code}>
                  {LOCALE_NAMES[code]}
                </option>
              ))}
            </select>
          </Field>

          <div className="flex flex-wrap items-center gap-4">
            <ActionButton pendingLabel={t.saving}>{t.save}</ActionButton>
            <ActionOutcome saved={t.saved} elsewhere={["invalid_feedback_url"]} />
          </div>
        </>
      )}
    </ActionForm>
  );
}
