import { API_PREFIX } from "@/lib/api/client";

import { resumable } from "./destination";

/** The providers this release knows, in the order they are shown. */
export const PROVIDER_IDS = ["microsoft", "google", "github"] as const;

export type ProviderId = (typeof PROVIDER_IDS)[number];

/** Brand names: shown as they are in every locale. */
export const PROVIDER_NAMES: Record<ProviderId, string> = {
  microsoft: "Microsoft",
  google: "Google",
  github: "GitHub",
};

/** `connector.MaxReturnPathLength` in the Go service. */
const MAX_RETURN_PATH_LENGTH = 512;

/**
 * Where a password sign-in may resume (`resumable`), and within the backend's
 * own rule (`connector.ReturnPath`): printable ASCII with no space or
 * backslash, which browsers read as a slash. Anything else is left off, and
 * the backend falls back to `/login`.
 */
function returnable(next: string): boolean {
  return (
    resumable(next) !== null &&
    next.length <= MAX_RETURN_PATH_LENGTH &&
    /^[\x21-\x7e]*$/.test(next) &&
    !next.includes("\\")
  );
}

/**
 * The start link for a provider, returning to `next` when it is a path on this
 * site. Signing in through a provider starts on the API, which redirects to
 * the provider; the callback ends at the return path, at `/login` without one
 * (the page sends a signed-in visitor on, as after a password sign-in), or at
 * `/login?error=<code>`.
 */
export function externalStartHref(provider: ProviderId, next: string | undefined): string {
  const start = `${API_PREFIX}/auth/${provider}/start`;
  if (!next || !returnable(next)) return start;
  return `${start}?return=${encodeURIComponent(next)}`;
}
