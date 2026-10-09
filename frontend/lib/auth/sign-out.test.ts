import { describe, expect, test } from "vitest";

import { signOut } from "./sign-out";

describe("signOut", () => {
  test("asks the API to end the session, then forgets it here", async () => {
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
    // The API answers an unknown token with 204.
    await expect(
      signOut({
        endSession: async () => undefined,
        clearCookie: () => {},
      }),
    ).resolves.toBeUndefined();
  });
});
