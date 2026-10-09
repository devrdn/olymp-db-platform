import { describe, expect, test } from "vitest";

import {
  INGRESS_HEADER,
  forwardedHeaders,
  headedForApi,
  incomingRequestHeaders,
  vouchedByIngress,
} from "./forwarded";

describe("forwardedHeaders", () => {
  const SECRET = "an-ingress-secret-of-at-least-32-characters";
  const from = (values: Record<string, string>) => (name: string) => values[name] ?? null;
  const viaProxy = (values: Record<string, string>) =>
    from({ [INGRESS_HEADER]: SECRET, ...values });

  test("hands on the address the ingress proxy established", () => {
    const headers = forwardedHeaders(viaProxy({ "x-forwarded-for": "203.0.113.7" }), SECRET);

    expect(headers).toEqual({ "x-forwarded-for": "203.0.113.7" });
  });

  test("keeps every hop, because the API decides which ones to believe", () => {
    const headers = forwardedHeaders(
      viaProxy({ "x-forwarded-for": "203.0.113.7, 172.28.0.2" }),
      SECRET,
    );

    expect(headers["x-forwarded-for"]).toBe("203.0.113.7, 172.28.0.2");
  });

  test("carries the browser's user agent for the audit trail", () => {
    const headers = forwardedHeaders(
      viaProxy({ "x-forwarded-for": "203.0.113.7", "user-agent": "Mozilla/5.0" }),
      SECRET,
    );

    expect(headers["user-agent"]).toBe("Mozilla/5.0");
  });

  test("never hands the proxy's secret on to the API", () => {
    const headers = forwardedHeaders(viaProxy({ "x-forwarded-for": "203.0.113.7" }), SECRET);

    expect(Object.keys(headers)).not.toContain(INGRESS_HEADER);
    expect(Object.values(headers).join(" ")).not.toContain(SECRET);
  });

  test("sends nothing when there is nothing", () => {
    expect(forwardedHeaders(from({}), SECRET)).toEqual({});
  });

  test("drops a chain the proxy did not vouch for", () => {
    const headers = forwardedHeaders(
      from({ "x-forwarded-for": "10.20.30.40", "user-agent": "Mozilla/5.0" }),
      SECRET,
    );

    expect(headers).toEqual({});
  });

  test("drops a chain that arrives with the wrong secret", () => {
    const headers = forwardedHeaders(
      from({ "x-forwarded-for": "10.20.30.40", [INGRESS_HEADER]: `${SECRET}x` }),
      SECRET,
    );

    expect(headers).toEqual({});
  });

  test("drops every chain when this server has no secret to check against", () => {
    for (const configured of [undefined, "", "short"]) {
      const headers = forwardedHeaders(
        from({ "x-forwarded-for": "10.20.30.40", [INGRESS_HEADER]: configured ?? "" }),
        configured,
      );

      expect(headers).toEqual({});
    }
  });

  test("drops a chain that does not look like addresses at all", () => {
    // fetch throws on control characters in a header.
    const headers = forwardedHeaders(viaProxy({ "x-forwarded-for": "gotcha\r\nhost: evil" }), SECRET);

    expect(headers["x-forwarded-for"]).toBeUndefined();
  });
});

describe("vouchedByIngress", () => {
  const SECRET = "an-ingress-secret-of-at-least-32-characters";

  test("accepts exactly the configured secret", () => {
    expect(vouchedByIngress(SECRET, SECRET)).toBe(true);
  });

  test.each([
    ["absent", null],
    ["empty", ""],
    ["a prefix", SECRET.slice(0, -1)],
    ["longer", `${SECRET}!`],
    ["different", "x".repeat(SECRET.length)],
  ])("refuses a presented secret that is %s", (_, presented) => {
    expect(vouchedByIngress(presented, SECRET)).toBe(false);
  });

  test("refuses everything when the configured secret is missing or too short", () => {
    expect(vouchedByIngress("", "")).toBe(false);
    expect(vouchedByIngress("short", "short")).toBe(false);
    expect(vouchedByIngress("anything", undefined)).toBe(false);
  });
});

describe("incomingRequestHeaders", () => {
  const SECRET = "an-ingress-secret-of-at-least-32-characters";

  test("removes every forwarded address header nobody vouched for, and keeps the rest", () => {
    const incoming = new Headers({
      "x-forwarded-for": "10.20.30.40",
      "x-real-ip": "10.20.30.41",
      forwarded: "for=10.20.30.42",
      cookie: "dbcontest_session=opaque",
      accept: "image/png",
    });

    const outgoing = incomingRequestHeaders(incoming, SECRET, { towardsApi: false });

    expect(outgoing.get("x-forwarded-for")).toBeNull();
    expect(outgoing.get("x-real-ip")).toBeNull();
    expect(outgoing.get("forwarded")).toBeNull();
    expect(outgoing.get("cookie")).toBe("dbcontest_session=opaque");
    expect(outgoing.get("accept")).toBe("image/png");
  });

  test("keeps a vouched chain, and the secret only where the server checks it again", () => {
    const incoming = new Headers({ "x-forwarded-for": "203.0.113.7", [INGRESS_HEADER]: SECRET });

    const towardsApi = incomingRequestHeaders(incoming, SECRET, { towardsApi: true });
    const towardsScreen = incomingRequestHeaders(incoming, SECRET, { towardsApi: false });

    expect(towardsApi.get("x-forwarded-for")).toBe("203.0.113.7");
    expect(towardsApi.get(INGRESS_HEADER)).toBeNull();
    expect(towardsScreen.get("x-forwarded-for")).toBe("203.0.113.7");
    expect(towardsScreen.get(INGRESS_HEADER)).toBe(SECRET);
  });

  test("does not change the headers it was given", () => {
    const incoming = new Headers({ "x-forwarded-for": "10.20.30.40" });

    incomingRequestHeaders(incoming, SECRET, { towardsApi: true });

    expect(incoming.get("x-forwarded-for")).toBe("10.20.30.40");
  });
});

describe("headedForApi", () => {
  test.each(["/api/v1/auth/login", "/API/v1/auth/login", "/Api/V1/x", "/api/"])("recognises %s", (path) => {
    expect(headedForApi(path)).toBe(true);
  });

  test.each(["/contests", "/apix/v1", "/login", "/"])("does not claim %s", (path) => {
    expect(headedForApi(path)).toBe(false);
  });
});
