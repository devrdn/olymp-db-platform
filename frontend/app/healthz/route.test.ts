import { describe, expect, test, vi } from "vitest";

// Liveness must not touch the API; any API call fails the test.
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

  // Prerendered, it would say "ok" whether or not the server runs.
  test("is rendered by the running server, not at build time", () => {
    expect(dynamic).toBe("force-dynamic");
  });
});
