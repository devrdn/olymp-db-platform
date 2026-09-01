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
  resetPassword: boolean;
  roles: boolean;
  profile: boolean;
};

export function offeredActions(
  account: { id: string; status: AccountStatus },
  viewerId: string,
): Offered {
  const isSelf = account.id === viewerId;

  return {
    // Blocking your own account locks the installation out of itself, which
    // is why the service refuses it. Offering it and reporting the refusal
    // afterwards would mean finding out by pressing.
    block: !isSelf && account.status === "active",
    unblock: !isSelf && account.status === "blocked",
    // Recoverable, and on your own account too: whoever asks is handed the
    // new password, so there is nothing to withhold.
    resetPassword: true,
    // Allowed on your own record. Demoting yourself retires your sessions and
    // you find out at once, which is honest — and forbidding it would leave
    // the last administrator unable to correct their own row.
    roles: true,
    profile: true,
  };
}
