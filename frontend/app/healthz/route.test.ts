import { describe, expect, test, vi } from "vitest";

// Liveness must not depend on the API: the container's healthcheck asks this
// every fifteen seconds, and a probe that reached the API would fill its log
// and turn this container unhealthy whenever the API is. Any import of the
// API client is a failure here, not a dependency to fake.
const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));

import { dynamic, GET } from "./route";

describe("GET /healthz", () => {
  test("answers 200 ok from this server alone", async () => {
    const response = GET();
    expect(response.status).toBe(200);
    expect(await response.text()).toBe("ok");
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("sets no cookie and is never cached", () => {
    const response = GET();
    expect(response.headers.get("set-cookie")).toBeNull();
    expect(response.headers.get("cache-control")).toBe("no-store");
  });

  // Prerendered at build time, the answer would be a file that says "ok"
  // whether or not the server behind it is running.
  test("is rendered by the running server, not at build time", () => {
    expect(dynamic).toBe("force-dynamic");
  });
});
