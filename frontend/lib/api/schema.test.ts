import { describe, expect, it } from "vitest";

import { gameSchemaSchema } from "./schema";

describe("the game schema wire shape", () => {
  it("reads a table with a foreign key", () => {
    const parsed = gameSchemaSchema.parse({
      tables: [
        {
          name: "guests",
          columns: [
            { name: "id", type: "uuid", nullable: false, references: "" },
            { name: "room_id", type: "uuid", nullable: true, references: "rooms" },
          ],
        },
      ],
      truncated: false,
    });

    expect(parsed.tables[0].columns[1]).toEqual({
      name: "room_id",
      type: "uuid",
      nullable: true,
      references: "rooms",
    });
  });

  it("reads each table's primary key and foreign keys, composite ones in order", () => {
    const parsed = gameSchemaSchema.parse({
      tables: [
        {
          name: "rooms",
          columns: [],
          primary_key: ["id"],
          foreign_keys: [
            {
              name: "rooms_on_a_floor",
              columns: ["lvl", "bld"],
              referenced_table: "floors",
              referenced_columns: ["level", "building"],
            },
          ],
        },
      ],
      truncated: false,
    });

    expect(parsed.tables[0].primary_key).toEqual(["id"]);
    expect(parsed.tables[0].foreign_keys).toEqual([
      { name: "rooms_on_a_floor", columns: ["lvl", "bld"], referenced_table: "floors", referenced_columns: ["level", "building"] },
    ]);
  });

  // An API deployed before keys were described still answers; its tables have none.
  it("reads a table without key lists", () => {
    const parsed = gameSchemaSchema.parse({ tables: [{ name: "plain", columns: [] }], truncated: false });

    expect(parsed.tables[0].primary_key ?? []).toEqual([]);
    expect(parsed.tables[0].foreign_keys ?? []).toEqual([]);
  });

  it("refuses a foreign key with no referenced table", () => {
    expect(() =>
      gameSchemaSchema.parse({
        tables: [{ name: "t", columns: [], foreign_keys: [{ name: "k", columns: ["a"], referenced_columns: ["id"] }] }],
        truncated: false,
      }),
    ).toThrow();
  });

  // The API never sends null for a list; accepting one would hide the day it did.
  it("refuses a null table list rather than rendering nothing", () => {
    expect(() => gameSchemaSchema.parse({ tables: null, truncated: false })).toThrow();
  });

  it("refuses a column with no type", () => {
    expect(() =>
      gameSchemaSchema.parse({
        tables: [{ name: "guests", columns: [{ name: "id", nullable: false, references: "" }] }],
        truncated: false,
      }),
    ).toThrow();
  });
});
