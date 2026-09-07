import "@testing-library/jest-dom/vitest";

import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// Testing Library only auto-cleans when Vitest globals are enabled. They are
// not, so the previous render would otherwise stay in the document and the
// next query would find two of everything.
afterEach(cleanup);

/**
 * jsdom does not lay out text, so `Range` — unlike `Element` — ships with no
 * `getClientRects`/`getBoundingClientRect` at all (`Element`'s own versions
 * exist and return a zero rect; `Range`'s are simply absent). CodeMirror 6
 * calls the `Range` ones on every view update, from a `requestAnimationFrame`
 * callback this test file never touches, to measure what it just drew — so
 * without a stub, any test that mounts `CodeEditor` (code-editor.test.tsx,
 * console.test.tsx) throws an *uncaught* `TypeError` well after the test that
 * triggered it has already reported passing, which Vitest correctly refuses
 * to treat as unrelated noise. The zero rectangle costs nothing here: no test
 * in this project asserts where a character was drawn on screen — that is
 * exactly the "jsdom cannot show you any of this" the component's own report
 * names, and is why the real-browser check exists.
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
