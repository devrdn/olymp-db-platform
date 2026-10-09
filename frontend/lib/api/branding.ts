import { serverRequest } from "./server";
import { settingsSchema, type Settings } from "./settings";

/**
 * The installation's branding for every screen's frame, from the public
 * allow-listed endpoint (the sign-in screen needs it before any session). A
 * failure falls back to empty values: the name is chrome, and must not take
 * down the sign-in form.
 */
export async function branding(): Promise<Settings> {
  return serverRequest("/settings")
    .then((payload) => settingsSchema.parse(payload))
    .catch(() => ({ name: "", contact: "", images: {} }));
}
