import { describe, expect, test } from "vitest";

import { forwardedHeaders } from "./forwarded";

/**
 * Every request the Go API ever sees comes from this server, not from the
 * browser: sign-in, enrolment, every admin action is a Server Action that
 * dials the API itself. Without these headers the API's view of "the client"
 * is this process — the per-address login throttle becomes one counter of 30
 * attempts shared by the whole installation, a contest's network restriction
 * compares against the web container's address, and the audit trail records
 * the proxy for every action (the "::1" actually seen in it).
 */
describe("forwardedHeaders", () => {
  const from = (values: Record<string, string>) => (name: string) => values[name] ?? null;

  test("hands on the address chain the ingress proxy established", () => {
    const headers = forwardedHeaders(from({ "x-forwarded-for": "203.0.113.7" }));

    expect(headers).toEqual({ "x-forwarded-for": "203.0.113.7" });
  });

  test("keeps every hop, because the API decides which ones to believe", () => {
    // Right to left, skipping its trusted proxies: that walk is the API's
    // job, and it needs the whole chain to do it.
    const headers = forwardedHeaders(
      from({ "x-forwarded-for": "203.0.113.7, 172.28.0.2" }),
    );

    expect(headers["x-forwarded-for"]).toBe("203.0.113.7, 172.28.0.2");
  });

  test("carries the browser's user agent for the audit trail", () => {
    const headers = forwardedHeaders(
      from({ "x-forwarded-for": "203.0.113.7", "user-agent": "Mozilla/5.0" }),
    );

    expect(headers["user-agent"]).toBe("Mozilla/5.0");
  });

  test("sends nothing when there is nothing", () => {
    // A development request that never went through the proxy has no chain.
    // Inventing one here would be this server vouching for an address it
    // does not know.
    expect(forwardedHeaders(from({}))).toEqual({});
  });

  test("drops a chain that does not look like addresses at all", () => {
    // The value is written into an outgoing header. fetch refuses control
    // characters by throwing — which would turn hostile input into a failed
    // sign-in — and anything else unparseable is noise the API would discard
    // anyway, so it is dropped here, quietly, instead.
    const headers = forwardedHeaders(from({ "x-forwarded-for": "gotcha\r\nhost: evil" }));

    expect(headers["x-forwarded-for"]).toBeUndefined();
  });
});
