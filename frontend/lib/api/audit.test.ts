import { describe, expect, test } from "vitest";

import { auditSearch, blockedProblems, dayBounds, summariseChanges } from "./audit";

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

describe("summariseChanges", () => {
  test("lists the fields that moved, with what they were", () => {
    const { changes } = summariseChanges({
      changes: {
        ends_at: { from: "2026-11-08T19:30:00Z", to: "2026-11-08T22:30:00Z" },
        enrollment: { from: "invite_only", to: "open" },
      },
    });

    expect(changes.map((c) => c.field)).toEqual(["ends_at", "enrollment"]);
    expect(changes[1]).toEqual({ field: "enrollment", from: "invite_only", to: "open" });
  });

  test("says when a save moved nothing", () => {
    // Otherwise it is indistinguishable from an edit the reader cannot see.
    expect(summariseChanges({ changed: false })).toEqual({ changes: [], unchanged: true });
  });

  test("renders an emptied list as an absence rather than as nothing at all", () => {
    const { changes } = summariseChanges({
      changes: { allowed_cidrs: { from: ["10.20.0.0/16"], to: [] } },
    });

    expect(changes[0]).toEqual({ field: "allowed_cidrs", from: "10.20.0.0/16", to: "—" });
  });

  test("says a value was too large rather than pretending it was empty", () => {
    const { changes } = summariseChanges({
      changes: { allowed_cidrs: { from: [], to: { omitted_bytes: 6000 } } },
    });

    expect(changes[0].to).toBe("6000 B");
  });

  test("has nothing to say about an entry that records no change set", () => {
    expect(summariseChanges({ login: "root" })).toEqual({ changes: [], unchanged: false });
    expect(summariseChanges(undefined)).toEqual({ changes: [], unchanged: false });
  });
});

describe("blockedProblems", () => {
  test("reads the codes off a contest.start_blocked entry's payload", () => {
    expect(blockedProblems({ problems: ["no_story", "no_questions"] })).toEqual([
      "no_story",
      "no_questions",
    ]);
  });

  test("has nothing to say about an entry that carries no problems", () => {
    expect(blockedProblems({ changes: {} })).toEqual([]);
    expect(blockedProblems(undefined)).toEqual([]);
  });

  test("drops anything that is not a string, rather than rendering it raw", () => {
    // The payload is read straight off the wire; a shape this reader does not
    // expect must not become a React child.
    expect(blockedProblems({ problems: ["no_story", 12, null] })).toEqual(["no_story"]);
  });
});
