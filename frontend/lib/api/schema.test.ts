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

  // The API never sends null for either list (participant_handler.go's own
  // rule), and a client that quietly accepted one would hide the day it did.
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
