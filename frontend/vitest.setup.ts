import "@testing-library/jest-dom/vitest";

import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// Testing Library auto-cleans only with Vitest globals, which are off.
afterEach(cleanup);

/**
 * jsdom's `Range` has no `getClientRects`/`getBoundingClientRect`. CodeMirror
 * calls them from `requestAnimationFrame` after each update, so without a stub
 * a test mounting `CodeEditor` throws an uncaught `TypeError` after it has
 * passed. No test asserts on-screen positions, so a zero rectangle is enough.
 */
if (typeof Range !== "undefined" && !Range.prototype.getClientRects) {
  const zeroRect = (): DOMRect => ({
    x: 0,
    y: 0,
    width: 0,
    height: 0,
    top: 0,
    right: 0,
    bottom: 0,
    left: 0,
    toJSON() {
      return this;
    },
  });
  Range.prototype.getClientRects = () => [zeroRect()] as unknown as DOMRectList;
  Range.prototype.getBoundingClientRect = zeroRect;
}
