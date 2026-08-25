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

export function destinationAfterLogin(identity: Identity): string {
  // A one-time password blocks every other request with password_change_required,
  // so any other destination would bounce straight back.
  if (identity.mustChangePassword) return "/password";

  const isStaff = identity.permissions.some((held) => STAFF_PERMISSIONS.includes(held));
  return isStaff ? "/contests" : "/my";
}
