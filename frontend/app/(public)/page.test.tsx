import { render } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";

// A Server Component: the API and the request are faked.
const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("@/lib/api/branding", () => ({
  branding: async () => ({ name: "USM", contact: "", feedback: "", images: {}, archive: false }),
}));
vi.mock("next/headers", () => ({ cookies: async () => ({ has: () => false }) }));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});
vi.mock("@/lib/theme/server", () => ({ activeTheme: async () => "light" }));

import HomePage from "./page";

afterEach(() => {
  vi.restoreAllMocks();
});

describe("the front page's reads", () => {
  test("log a failure on one line without what the failure said, and still render", async () => {
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});
    serverRequest.mockRejectedValue(
      new TypeError('Headers.append: "dbcontest_session=secret-token\nFAKE LINE" is an invalid header value.'),
    );

    const { container } = render(await HomePage());

    expect(container).not.toBeEmptyDOMElement();
    const lines = logged.mock.calls.map((call) => call.map(String).join(" "));
    expect(lines).toHaveLength(2);
    expect(lines.join("\n")).toContain("/public/stats");
    for (const line of lines) {
      expect(line).not.toContain("secret-token");
      expect(line).not.toMatch(/[\r\n]/);
    }
  });

  test("write no log line for a visitor past the public read budget, which anyone can reach at will", async () => {
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});
    serverRequest.mockRejectedValue(new ApiError("public_too_often", 429, "Too often"));

    const { container } = render(await HomePage());

    expect(container).not.toBeEmptyDOMElement();
    expect(logged).not.toHaveBeenCalled();
  });
});
