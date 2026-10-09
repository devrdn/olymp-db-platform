import { describe, expect, test } from "vitest";

import { offeredActions } from "./offered";

const other = { id: "u-2", status: "active" as const };
const self = { id: "u-1", status: "active" as const };
const viewer = "u-1";

describe("offeredActions", () => {
  test("offers blocking somebody else's active account", () => {
    expect(offeredActions(other, viewer)).toMatchObject({ block: true, unblock: false });
  });

  test("offers unblocking instead once the account is blocked", () => {
    // Block and unblock are never both offered.
    expect(offeredActions({ ...other, status: "blocked" }, viewer)).toMatchObject({
      block: false,
      unblock: true,
    });
  });

  test("never offers to block your own account", () => {
    // The API refuses a self-block, so it is not offered.
    expect(offeredActions(self, viewer)).toMatchObject({ block: false, unblock: false });
  });

  test("still offers a password reset on your own account", () => {
    // Resetting your own password is fine: you receive the new one.
    expect(offeredActions(self, viewer).resetPassword).toBe(true);
  });

  test("offers roles and profile edits on any account, including your own", () => {
    // Demoting yourself is allowed, or the last administrator could not fix
    // their own record.
    expect(offeredActions(self, viewer)).toMatchObject({ roles: true, profile: true });
  });
});

describe("offeredActions, when the viewer is not known", () => {
  test("offers no block at all rather than offering it on everyone", () => {
    // An empty `viewerId` fails closed, or every account would look blockable.
    expect(offeredActions({ id: "u-1", status: "active" }, "")).toMatchObject({
      block: false,
      unblock: false,
    });
  });

  test("still offers what cannot lock anybody out", () => {
    expect(offeredActions({ id: "u-1", status: "active" }, "")).toMatchObject({
      resetPassword: true,
      roles: true,
    });
  });
});

describe("offeredActions, for a deleted account", () => {
  const deleted = { id: "u-2", status: "deleted" as const };

  test("offers restore and nothing that assumes the account can still be reached", () => {
    const offered = offeredActions(deleted, viewer);

    expect(offered.restore).toBe(true);
    expect(offered.block).toBe(false);
    expect(offered.unblock).toBe(false);
    expect(offered.resetPassword).toBe(false);
    expect(offered.roles).toBe(false);
    // A deleted account has no lockout to clear.
    expect(offered.unlockSignIn).toBe(false);
    // The server refuses editing a deleted account (ErrAccountDeleted).
    expect(offered.profile).toBe(false);
  });

  test("offers delete on an active or a blocked account, but not on a deleted one", () => {
    expect(offeredActions(other, viewer).delete).toBe(true);
    expect(offeredActions({ ...other, status: "blocked" }, viewer).delete).toBe(true);
    expect(offeredActions(deleted, viewer).delete).toBe(false);
  });

  test("never offers to restore an account that is not deleted", () => {
    expect(offeredActions(other, viewer).restore).toBe(false);
    expect(offeredActions({ ...other, status: "blocked" }, viewer).restore).toBe(false);
  });

  test("never offers to delete your own account, the same rule as blocking", () => {
    expect(offeredActions({ ...self, status: "active" }, viewer).delete).toBe(false);
  });

  test("offers no restore at all when the viewer is not known", () => {
    // Fails closed for an unknown viewer, as block does.
    expect(offeredActions(deleted, "").restore).toBe(false);
  });
});

describe("offeredActions, clearing a sign-in lockout", () => {
  test("is offered on an active or blocked account, and on your own", () => {
    // Harmless, so offered on your own account too.
    expect(offeredActions(other, viewer).unlockSignIn).toBe(true);
    expect(offeredActions({ ...other, status: "blocked" }, viewer).unlockSignIn).toBe(true);
    expect(offeredActions(self, viewer).unlockSignIn).toBe(true);
    expect(offeredActions(other, "").unlockSignIn).toBe(true);
  });
});
