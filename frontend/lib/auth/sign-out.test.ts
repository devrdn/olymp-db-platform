import { describe, expect, test } from "vitest";

import { signOut } from "./sign-out";

describe("signOut", () => {
  test("asks the API to end the session, then forgets it here", async () => {
    // Both halves are needed. The server holds the session, so only it can
    // withdraw one; our own copy of the cookie was written by the sign-in
    // action and the API has no way to reach it.
    const done: string[] = [];

    await signOut({
      endSession: async () => {
        done.push("api");
      },
      clearCookie: () => done.push("cookie"),
    });

    expect(done).toEqual(["api", "cookie"]);
  });

  test("forgets the session here even when the API could not be reached", async () => {
    // The worst outcome for a button marked "sign out" is staying signed in.
    // If the API is down, the session on this browser is the part we can still
    // end — and the next request will be refused anyway, so leaving the cookie
    // in place buys nothing and costs the user their trust in the button.
    let cleared = false;

    await signOut({
      endSession: async () => {
        throw new Error("connect ECONNREFUSED");
      },
      clearCookie: () => {
        cleared = true;
      },
    });

    expect(cleared).toBe(true);
  });

  test("says nothing about a session that had already expired", async () => {
    // The API answers an unknown token with 204: logging out twice, or with a
    // stale cookie, is an ordinary thing for a browser to do. Nothing here
    // should turn that into an error the visitor has to read.
    await expect(
      signOut({
        endSession: async () => undefined,
        clearCookie: () => {},
      }),
    ).resolves.toBeUndefined();
  });
});
