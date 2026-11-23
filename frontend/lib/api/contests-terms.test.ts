import { describe, expect, test } from "vitest";

import { coverHref } from "./contests-terms";

describe("coverHref", () => {
  const contestId = "f767af3b-f135-40d2-a3a6-82d368de1004";
  const hash = "9f2c1ab4d5e6f70819a2b3c4d5e6f7081920a2b3c4d5e6f70819a2b3c4d5e6f7";

  test("carries the hash, so a replaced cover is a different address", () => {
    expect(coverHref(contestId, hash)).toBe(
      `/api/v1/public/contests/${contestId}/cover?size=800&v=${hash}`,
    );
  });

  test("asks for the card's rendition unless the caller wants the large one", () => {
    expect(coverHref(contestId, hash)).toContain("size=800");
    expect(coverHref(contestId, hash, 1600)).toContain("size=1600");
  });
});
