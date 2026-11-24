import { cache } from "react";

import { bestEffort } from "./outcome";
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
 * failure falls back to empty values, and is logged: the name is chrome, and
 * must not take down the sign-in form. React's `cache()` lets a page that
 * needs a value (the feedback address) share its layout's request.
 */
export const branding = cache(async (): Promise<Settings> => {
  if (kept && Date.now() - kept.at < KEPT_FOR_MS) return kept.settings;

  const settings = await bestEffort(async () => settingsSchema.parse(await serverRequest("/settings")), "/settings");
  if (!settings) return { name: "", contact: "", feedback: "", mailLanguage: "", images: {}, archive: false };
  kept = { settings, at: Date.now() };
  return settings;
});

/** For the settings actions, which run in this process, so a save shows on the next screen. */
export function forgetBranding(): void {
  kept = undefined;
}
