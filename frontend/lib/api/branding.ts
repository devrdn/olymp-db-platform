import { cache } from "react";

import { serverRequest } from "./server";
import { settingsSchema, type Settings } from "./settings";

/**
 * The installation's branding for every screen's frame, from the public
 * allow-listed endpoint (the sign-in screen needs it before any session). A
 * failure falls back to empty values: the name is chrome, and must not take
 * down the sign-in form. React's `cache()` lets a page that needs a value
 * (the feedback address) share its layout's request.
 */
export const branding = cache(async (): Promise<Settings> => {
  return serverRequest("/settings")
    .then((payload) => settingsSchema.parse(payload))
    .catch(() => ({ name: "", contact: "", feedback: "", images: {} }));
});
