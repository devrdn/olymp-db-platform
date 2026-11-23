import { describe, expect, test } from "vitest";

import { uploaded } from "./pdf-test-support";
import { titleSpace } from "./running-header";
import { HEADER, MEASURE } from "./theme";

describe("titleSpace", () => {
  test("gives the title the measure less the logo and the gap before it", () => {
    expect(titleSpace(null)).toBeCloseTo(MEASURE, 6);
    expect(titleSpace("mark")).toBeCloseTo(MEASURE - (28 * 90) / 116 - HEADER.titleGap, 6);
    expect(titleSpace(uploaded(480, 140))).toBeCloseTo(MEASURE - 96 - HEADER.titleGap, 6);
  });
});
