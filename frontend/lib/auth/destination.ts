/**
 * Where an account goes once it has signed in. Routing reads permissions,
 * never roles, as the server's middleware does, so a role added as data needs
 * no change here.
 */

/** What routing needs to know about the signed-in account. */
export type Identity = { mustChangePassword: boolean; permissions: string[] };

/**
 * Any of these sends the account to the constructor. Per-contest permissions
 * are absent: they say nothing about where to land.
 */
const STAFF_PERMISSIONS = [
  "contest.create",
  "contest.admin_all",
  "users.manage",
  "reports.view",
  "audit.view",
];

const SIGN_IN = "/login";

/**
 * The guard's `?next=`, if safe to obey. Only a path on this origin is taken,
 * or sign-in becomes an open redirect; `//host` and `/\host` are
 * protocol-relative to browsers and refused. `/` (the showcase) and `/login`
 * are refused as pointless destinations.
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

/** An account's home; also used by screens both audiences share, such as the profile. */
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
