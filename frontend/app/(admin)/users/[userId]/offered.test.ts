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
    // Two states, one control: a page carrying both is a page where one of
    // them always refuses.
    expect(offeredActions({ ...other, status: "blocked" }, viewer)).toMatchObject({
      block: false,
      unblock: true,
    });
  });

  test("never offers to block your own account", () => {
    // The API refuses it — an administrator who blocks themselves locks the
    // installation — and a button that exists only to be refused teaches
    // nothing. Offering it and reporting the error afterwards is worse: the
    // reader finds out by pressing.
    expect(offeredActions(self, viewer)).toMatchObject({ block: false, unblock: false });
  });

  test("still offers a password reset on your own account", () => {
    // Not the same rule. Resetting your own password is recoverable — you are
    // handed the new one — so there is no reason to withhold it.
    expect(offeredActions(self, viewer).resetPassword).toBe(true);
  });

  test("offers roles and profile edits on any account, including your own", () => {
    // Demoting yourself is allowed: the server bumps the session generation
    // and you find out immediately, which is honest. Forbidding it would make
    // the last administrator unable to fix their own record.
    expect(offeredActions(self, viewer)).toMatchObject({ roles: true, profile: true });
  });
});

describe("offeredActions, when the viewer is not known", () => {
  test("offers no block at all rather than offering it on everyone", () => {
    // `viewerId` is empty when the page could not learn who is looking. With
    // an empty string nothing equals it, so every account — including the
    // reader's own — would look blockable. The server refuses a self-block
    // either way; what this avoids is a screen that invites the press.
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
