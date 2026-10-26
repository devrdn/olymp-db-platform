import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import { readPositions, schemaIdentity, writePositions } from "./schema-diagram-positions";

const names = ["floors", "rooms", "notes"];

beforeEach(() => {
  window.localStorage.clear();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("the diagram's remembered positions", () => {
  test("are read back for the same contest and the same tables", () => {
    writePositions("c1", names, new Map([["rooms", { x: 400, y: -20 }]]));

    expect(readPositions("c1", names)).toEqual(new Map([["rooms", { x: 400, y: -20 }]]));
    expect(readPositions("c2", names).size).toBe(0);
  });

  test("whatever order the tables come in", () => {
    expect(schemaIdentity(["b", "a"])).toBe(schemaIdentity(["a", "b"]));
    expect(schemaIdentity(["a", "b"])).not.toBe(schemaIdentity(["a", "b", "c"]));
    // NUL joins the names, so the boundary between them counts.
    expect(schemaIdentity(["ab", "c"])).not.toBe(schemaIdentity(["a", "bc"]));
  });

  test("are ignored once the schema has other tables", () => {
    writePositions("c1", names, new Map([["rooms", { x: 400, y: 20 }]]));

    expect(readPositions("c1", ["rooms", "guests"]).size).toBe(0);
  });

  test("forget everything when nothing is moved", () => {
    writePositions("c1", names, new Map([["rooms", { x: 1, y: 2 }]]));
    writePositions("c1", names, new Map());

    expect(window.localStorage.length).toBe(0);
  });

  test.each([
    ["not JSON", "{"],
    ["another version", JSON.stringify({ v: 99, schema: schemaIdentity(names), tables: { rooms: [1, 2] } })],
    ["no tables", JSON.stringify({ v: 1, schema: schemaIdentity(names) })],
  ])("read nothing from a stored value that is %s", (_, raw) => {
    window.localStorage.setItem("dbcontest.diagram.c1", raw);

    expect(readPositions("c1", names).size).toBe(0);
  });

  test("skip a table that is not in the schema or not a pair of numbers", () => {
    window.localStorage.setItem(
      "dbcontest.diagram.c1",
      JSON.stringify({
        v: 1,
        schema: schemaIdentity(names),
        tables: { rooms: [10, 20], floors: ["1", 2], notes: [1e12, 0], gone: [1, 1] },
      }),
    );

    expect(readPositions("c1", names)).toEqual(new Map([["rooms", { x: 10, y: 20 }]]));
  });

  test("cost nothing where storage is refused", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("denied");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("denied");
    });

    expect(() => writePositions("c1", names, new Map([["rooms", { x: 1, y: 2 }]]))).not.toThrow();
    expect(readPositions("c1", names).size).toBe(0);
  });
});
