import type { AccountStatus } from "@/lib/api/accounts";

/**
 * What this screen may offer for this account.
 *
 * Split out from the component because it is the only part with decisions in
 * it, and the decisions are worth pinning: a control that exists only to be
 * refused teaches somebody that a thing is possible and then that it is not,
 * which is the rudest way to state a rule.
 *
 * The server still enforces every one of these. This decides what to *show* —
 * hiding a control is never the guarantee, and `users.Service` refuses a
 * self-block whatever this returns.
 */
export type Offered = {
  block: boolean;
  unblock: boolean;
  /** Soft-deletes the account. Offered on anything that is not deleted yet. */
  delete: boolean;
  /** The one action a deleted account can take. */
  restore: boolean;
  resetPassword: boolean;
  roles: boolean;
  profile: boolean;
};

export function offeredActions(
  account: { id: string; status: AccountStatus },
  viewerId: string,
): Offered {
  // An empty viewer is "we could not find out who is looking". Nothing equals
  // an empty string, so treating it as "not you" would make every account
  // look blockable — including the reader's own. Unknown fails closed.
  const known = viewerId !== "";
  const isSelf = account.id === viewerId;
  const deleted = account.status === "deleted";

  return {
    // Blocking your own account locks the installation out of itself, which
    // is why the service refuses it. Offering it and reporting the refusal
    // afterwards would mean finding out by pressing.
    block: known && !isSelf && account.status === "active",
    unblock: known && !isSelf && account.status === "blocked",
    // A separate transition from blocking, not a third state of the same
    // toggle: an active or a blocked account can be deleted alike, so this is
    // offered whenever the account is not deleted yet rather than only beside
    // "block".
    delete: known && !isSelf && !deleted,
    // Deleted is the one status this offers a way out of. Every control below
    // it assumes the account can still be reached in some way — signed in
    // with a new password, given a role — and a deleted account cannot be.
    restore: known && !isSelf && deleted,
    // Recoverable, and on your own account too: whoever asks is handed the
    // new password, so there is nothing to withhold. Meaningless once the
    // account cannot sign in at all — nobody is left to hand it to.
    resetPassword: !deleted,
    // Allowed on your own record. Demoting yourself retires your sessions and
    // you find out at once, which is honest — and forbidding it would leave
    // the last administrator unable to correct their own row. A deleted
    // account holds no session to retire and no permission the roles matter
    // to.
    roles: !deleted,
    profile: true,
  };
}
