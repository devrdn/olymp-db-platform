import { describe, expect, test } from "vitest";

import { tableQuery } from "./table-query";

describe("tableQuery", () => {
  test("reads the first five rows of a table, without running anything", () => {
    expect(tableQuery("guests")).toBe("SELECT * FROM guests LIMIT 5;");
  });

  test("quotes a name that needs it", () => {
    expect(tableQuery("Order Items")).toBe('SELECT * FROM "Order Items" LIMIT 5;');
  });
});
