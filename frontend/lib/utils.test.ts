import { describe, expect, test } from "vitest";

import { cn } from "./utils";

/**
 * The scale is the project's own, so merge has to be told about it. Without
 * the extension every `text-<step>` looks like a colour to tailwind-merge and
 * loses to the colour written next to it — which is not an error anywhere, it
 * just renders at the wrong size.
 */
describe("cn", () => {
  test("keeps a type step and a colour together instead of dropping one", () => {
    expect(cn("font-mono text-label text-ink-3")).toBe("font-mono text-label text-ink-3");
    expect(cn("text-body text-ink-2")).toBe("text-body text-ink-2");
  });

  test("still lets a later step replace an earlier one", () => {
    expect(cn("text-body", "text-h2")).toBe("text-h2");
  });

  test("still lets a later colour replace an earlier one", () => {
    expect(cn("text-ink-3", "text-bad")).toBe("text-bad");
  });
});
