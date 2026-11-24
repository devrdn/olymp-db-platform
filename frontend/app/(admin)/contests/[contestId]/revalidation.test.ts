import { describe, expect, test } from "vitest";

import { contestChanged } from "./revalidation";

const contestId = "11111111-1111-1111-1111-111111111111";

describe("what a contest write makes stale", () => {
  test("is the workspace layout and every screen beneath it", () => {
    expect(contestChanged(contestId)).toEqual([{ layout: `/contests/${contestId}` }]);
  });

  test("is the register as well for a write that changes what it lists", () => {
    expect(contestChanged(contestId, { register: true })).toEqual(["/contests", { layout: `/contests/${contestId}` }]);
  });
});
