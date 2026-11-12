import { API_PREFIX } from "@/lib/api/client";

import { resumable } from "./destination";

/**
 * Signing in with Microsoft starts on the API, which redirects to Microsoft;
 * the callback ends at the return path, at `/login` without one (the page
 * sends a signed-in visitor on, as after a password sign-in), or at
 * `/login?error=<code>`.
 */
export const MICROSOFT_START = `${API_PREFIX}/auth/microsoft/start`;

/** `microsoft.MaxReturnPathLength` in the Go service. */
const MAX_RETURN_PATH_LENGTH = 512;

/**
 * Where a password sign-in may resume (`resumable`), and within the backend's
 * own rule (`microsoft.ReturnPath`): printable ASCII with no space or
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

/** The start link, returning to `next` when it is a path on this site. */
export function microsoftStartHref(next: string | undefined): string {
  if (!next || !returnable(next)) return MICROSOFT_START;
  return `${MICROSOFT_START}?return=${encodeURIComponent(next)}`;
}
