import { describe, expect, test } from "vitest";

import { auditSearch, dayBounds } from "./audit";

describe("auditSearch", () => {
  test("leaves out what was not asked for", () => {
    // An empty filter sent as `action=` would be a filter, not the absence of
    // one, and the API would answer with nothing.
    expect(auditSearch({}).toString()).toBe("limit=50");
  });

  test("carries every filter the API understands", () => {
    const search = auditSearch({
      actor: "a-1",
      action: "user.block",
      entity: "user",
      entityId: "u-1",
      from: "2026-03-01T00:00:00Z",
      to: "2026-03-02T00:00:00Z",
      offset: 50,
    });

    expect(search.get("actor")).toBe("a-1");
    expect(search.get("entity_id")).toBe("u-1");
    expect(search.get("offset")).toBe("50");
  });
});

describe("dayBounds", () => {
  test("takes a day to the instant after it, because `to` is exclusive", () => {
    // "Up to and including the 3rd" is midnight on the 4th. Left as the 3rd,
    // a whole day of entries would be missing from the answer and nothing
    // would say so.
    expect(dayBounds("", "2026-03-03")).toEqual({ to: "2026-03-04T00:00:00Z" });
  });

  test("takes the start of the day as it is", () => {
    expect(dayBounds("2026-03-01", "")).toEqual({ from: "2026-03-01T00:00:00Z" });
  });

  test("says nothing when no window was asked for", () => {
    expect(dayBounds("", "")).toEqual({});
  });

  test("crosses a month boundary without inventing a day", () => {
    expect(dayBounds("", "2026-03-31")).toEqual({ to: "2026-04-01T00:00:00Z" });
  });
});
