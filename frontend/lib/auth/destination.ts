/**
 * Where an account goes once it has signed in.
 *
 * There is one sign-in form: the API has no separate admin endpoint, and the
 * difference between a participant and staff only appears afterwards, in the
 * permissions `/auth/me` returns. Routing therefore reads permissions and
 * never roles, the same rule the server's middleware follows, so a new role
 * added as data needs no change here.
 */

export type Identity = { mustChangePassword: boolean; permissions: string[] };

/**
 * Holding any of these means the account has business in the constructor.
 * Scoped contest permissions are deliberately absent: they are granted per
 * contest, so they say nothing about where to land.
 */
const STAFF_PERMISSIONS = [
  "contest.create",
  "contest.admin_all",
  "users.manage",
  "reports.view",
  "audit.view",
];

/** Where a visitor is sent to obtain a session, and never sent back to. */
const SIGN_IN = "/login";

/**
 * The `?next=` the guard captured, if it is safe to obey.
 *
 * An unchecked `next` turns the sign-in page into an open redirect, which is
 * how a phishing link borrows a real domain: the address bar shows this
 * university, the destination does not. Only a path on this origin is
 * accepted, and the two forms browsers read as protocol-relative — `//host`
 * and `/\host`, since a backslash is normalised to a slash — are rejected
 * along with everything that is not a path at all.
 *
 * `/` and `/login` are refused for a duller reason: the first is the showcase,
 * which is not what somebody signing in is asking for, and the second is where
 * the visitor has just come from.
 */
function resumable(next: string | undefined): string | null {
  if (!next || next[0] !== "/") return null;
  if (next[1] === "/" || next[1] === "\\") return null;
  // A newline or a NUL in a Location header is a response-splitting attempt.
  if (/[\u0000-\u001f\u007f]/.test(next)) return null;

  const path = next.split(/[?#]/, 1)[0];
  if (path === "/") return null;
  if (path === SIGN_IN || path.startsWith(`${SIGN_IN}/`)) return null;

  return next;
}

/**
 * Where an account belongs when nothing more specific is known.
 *
 * Separate from the function below because two callers need it: the one that
 * decides where signing in lands, and any screen shared by both audiences —
 * the profile — which has to point its own mark somewhere. A participant sent
 * to the register would meet it scoped to contests they manage, which is
 * empty: an accurate answer to a question they never asked.
 */
export function homeFor(permissions: string[]): string {
  const isStaff = permissions.some((held) => STAFF_PERMISSIONS.includes(held));
  return isStaff ? "/contests" : "/my";
}

export function destinationAfterLogin(identity: Identity, next?: string): string {
  // A one-time password blocks every other request with password_change_required,
  // so any other destination would bounce straight back.
  if (identity.mustChangePassword) return "/password";

  const resumed = resumable(next);
  if (resumed) return resumed;

  return homeFor(identity.permissions);
}
