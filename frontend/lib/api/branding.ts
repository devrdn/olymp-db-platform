import { cache } from "react";

import { serverRequest } from "./server";
import { settingsSchema, type Settings } from "./settings";

const KEPT_FOR_MS = 30_000;

/**
 * Shared by every request this process serves, which is safe because the
 * answer is public and the same for every visitor. Only an answer is kept,
 * never the empty stand-in for a failure.
 */
let kept: { settings: Settings; at: number } | undefined;

/**
 * The installation's branding for every screen's frame, from the public
 * allow-listed endpoint (the sign-in screen needs it before any session). A
 * failure falls back to empty values: the name is chrome, and must not take
 * down the sign-in form. React's `cache()` lets a page that needs a value
 * (the feedback address) share its layout's request.
 */
export const branding = cache(async (): Promise<Settings> => {
  if (kept && Date.now() - kept.at < KEPT_FOR_MS) return kept.settings;

  return serverRequest("/settings")
    .then((payload) => {
      const settings = settingsSchema.parse(payload);
      kept = { settings, at: Date.now() };
      return settings;
    })
    .catch(() => ({ name: "", contact: "", feedback: "", mailLanguage: "", images: {} }));
});

/** For the settings actions, which run in this process, so a save shows on the next screen. */
export function forgetBranding(): void {
  kept = undefined;
}
