import { describe, expect, test } from "vitest";

import { identityFrom } from "./session";

/**
 * The distinction this file exists for.
 *
 * "The server says you are not signed in" and "the server did not answer" are
 * different facts, and only the first is a reason to send somebody to the
 * sign-in form. Collapsing them turns any hiccup — a restarted API, a dropped
 * connection, a 500 — into a forced sign-out, indistinguishable from a session
 * that really expired. That is the shape of the bug reported three times as
 * "it keeps throwing me to login".
 */
describe("identityFrom", () => {
  test("reports who the caller is when the server says so", async () => {
    const identity = await identityFrom(
      new Response(
        JSON.stringify({ id: "u1", login: "ivanov", full_name: "Ivan Ivanov", roles: ["student"] }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    expect(identity).toMatchObject({ login: "ivanov", fullName: "Ivan Ivanov", roles: ["student"] });
  });

  test("reports no session when the server says the session is no good", async () => {
    const identity = await identityFrom(
      new Response(JSON.stringify({ error: { code: "unauthenticated" } }), { status: 401 }),
    );

    expect(identity).toBeNull();
  });

  test("refuses to call an outage a sign-out", async () => {
    // A 500 means the server could not answer the question, not that it
    // answered "nobody". Returning null here is how one API restart signs
    // everybody out of a screen their session was perfectly good for.
    await expect(identityFrom(new Response("", { status: 500 }))).rejects.toThrow();
  });

  test("refuses to call an unreachable server a sign-out either", async () => {
    await expect(identityFrom(null)).rejects.toThrow();
  });

  test("falls back to the login when the account carries no name yet", async () => {
    const identity = await identityFrom(
      new Response(JSON.stringify({ id: "u1", login: "s.popescu" }), { status: 200 }),
    );

    expect(identity).toMatchObject({ login: "s.popescu", fullName: "", roles: [] });
  });
});
