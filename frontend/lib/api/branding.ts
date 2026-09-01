import { serverRequest } from "./server";
import { settingsSchema, type Settings } from "./settings";

/**
 * What the installation calls itself, for the frame every screen wears.
 *
 * Read from the public endpoint, which answers without a session — the
 * sign-in screen carries the name and is seen before anybody signs in. That
 * endpoint returns an allow-list rather than the table, so nothing private can
 * arrive here by accident.
 *
 * A failure is not worth a screen. The name is chrome: if the API cannot be
 * asked, the bar falls back to the product's own name and the page renders.
 * Throwing would mean an unreachable settings row taking down the sign-in
 * form, which is the one page somebody needs when things are going wrong.
 */
export async function branding(): Promise<Settings> {
  return serverRequest("/settings")
    .then((payload) => settingsSchema.parse(payload))
    .catch(() => ({ name: "", contact: "", images: {} }));
}
