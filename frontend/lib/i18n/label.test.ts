import { describe, expect, test } from "vitest";

import en from "./dictionaries/en";
import { labelFor } from "./label";

describe("labelFor", () => {
  test("is the section's text for a key it holds", () => {
    expect(labelFor(en.audit.entities, "contest")).toBe(en.audit.entities.contest);
  });

  test.each(["constructor", "__proto__", "toString", "hasOwnProperty", "not_a_key"])(
    "is nothing for %s, which the section does not hold itself",
    (key) => {
      expect(labelFor(en.audit.entities, key)).toBeUndefined();
    },
  );
});
