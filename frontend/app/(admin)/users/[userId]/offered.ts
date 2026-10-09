import type { AccountStatus } from "@/lib/api/accounts";

/**
 * What the screen offers for this account. A control that exists only to be
 * refused is worse than none. The server still enforces every rule;
 * `users.Service` refuses a self-block whatever this returns.
 */
export type Offered = {
  block: boolean;
  unblock: boolean;
  /** Soft-delete, offered on any account not yet deleted. */
  delete: boolean;
  /** The only action on a deleted account. */
  restore: boolean;
  resetPassword: boolean;
  /** Forgets the failed sign-in attempts counted against the account. */
  unlockSignIn: boolean;
  roles: boolean;
  profile: boolean;
};

export function offeredActions(
  account: { id: string; status: AccountStatus },
  viewerId: string,
): Offered {
  // An empty viewer id means unknown; treating it as "not you" would offer a
  // self-block. Unknown fails closed.
  const known = viewerId !== "";
  const isSelf = account.id === viewerId;
  const deleted = account.status === "deleted";

  return {
    // Blocking yourself could lock the installation out of itself; the service
    // refuses it.
    block: known && !isSelf && account.status === "active",
    unblock: known && !isSelf && account.status === "blocked",
    // Independent of blocking: active and blocked accounts can both be deleted.
    delete: known && !isSelf && !deleted,
    // Every other control assumes the account is reachable, which a deleted one
    // is not.
    restore: known && !isSelf && deleted,
    // Allowed on your own account, since the asker receives the password;
    // pointless once deleted.
    resetPassword: !deleted,
    // Harmless, so offered on your own account and for an unknown viewer.
    unlockSignIn: !deleted,
    // Allowed on your own record, or the last administrator could not fix their
    // own row.
    roles: !deleted,
    // The server refuses editing a deleted account (ErrAccountDeleted).
    profile: !deleted,
  };
}
