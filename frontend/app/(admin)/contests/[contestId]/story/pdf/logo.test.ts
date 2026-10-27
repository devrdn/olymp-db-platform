import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import { loadLogo } from "./logo";
import { COLOR } from "./theme";

/**
 * jsdom has neither image decoding nor a canvas, so both are stand-ins that
 * record what the drawing does: a picture of a chosen size that loads or fails,
 * and a canvas whose context logs each call in order.
 */
type Picture = { naturalWidth: number; naturalHeight: number; fails?: boolean };
let picture: Picture;
let calls: unknown[][];
let canvas: { width: number; height: number; getContext: (kind: string) => unknown; toDataURL: (...args: unknown[]) => string };
let context: Record<string, unknown> | null;

class FakeImage {
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  naturalWidth = picture.naturalWidth;
  naturalHeight = picture.naturalHeight;
  set src(value: string) {
    calls.push(["load", value]);
    queueMicrotask(() => (picture.fails ? this.onerror?.() : this.onload?.()));
  }
}

beforeEach(() => {
  picture = { naturalWidth: 480, naturalHeight: 140 };
  calls = [];
  context = {
    set fillStyle(value: string) {
      calls.push(["fillStyle", value]);
    },
    fillRect: (...args: unknown[]) => calls.push(["fillRect", ...args]),
    drawImage: (_image: unknown, ...args: unknown[]) => calls.push(["drawImage", ...args]),
  };
  canvas = {
    width: 0,
    height: 0,
    getContext: (kind: string) => (kind === "2d" ? context : null),
    toDataURL: (...args: unknown[]) => {
      calls.push(["toDataURL", ...args]);
      return `data:${String(args[0])};base64,AAAA`;
    },
  };
  vi.stubGlobal("Image", FakeImage);
  const create = document.createElement.bind(document);
  vi.spyOn(document, "createElement").mockImplementation(((tag: string) =>
    tag === "canvas" ? canvas : create(tag)) as typeof document.createElement);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("loadLogo", () => {
  test("fills the canvas with the paper's white before drawing, so transparency prints as paper", async () => {
    await loadLogo("/api/v1/settings/images/logo?v=1");

    const steps = calls.map(([name]) => name);
    expect(steps.indexOf("fillRect")).toBeLessThan(steps.indexOf("drawImage"));
    expect(calls).toContainEqual(["fillStyle", COLOR.paper]);
    expect(calls).toContainEqual(["fillRect", 0, 0, canvas.width, canvas.height]);
  });

  test("keeps the picture's proportions, its longer side 512 pixels", async () => {
    const wide = await loadLogo("/wide");
    expect(wide).toMatchObject({ width: 512, height: 149 });

    picture = { naturalWidth: 100, naturalHeight: 400 };
    const tall = await loadLogo("/tall");
    expect(tall).toMatchObject({ width: 128, height: 512 });
    expect(calls).toContainEqual(["drawImage", 0, 0, 128, 512]);
  });

  test("draws a vector picture that reports no size of its own as a square, not as nothing", async () => {
    picture = { naturalWidth: 0, naturalHeight: 0 };

    const logo = await loadLogo("/logo.svg");

    expect(logo).toMatchObject({ width: 512, height: 512 });
    expect(Number.isFinite(canvas.width) && canvas.width > 0).toBe(true);
  });

  test("hands back a JPEG, which the PDF library embeds without decoding", async () => {
    const logo = await loadLogo("/logo.webp");

    expect(calls).toContainEqual(["toDataURL", "image/jpeg", 0.95]);
    expect(logo.src.startsWith("data:image/jpeg;")).toBe(true);
  });

  test("a picture that cannot be loaded rejects, which the maker turns into a file without a logo", async () => {
    picture = { naturalWidth: 0, naturalHeight: 0, fails: true };

    await expect(loadLogo("/missing")).rejects.toThrow("the logo could not be loaded");
    expect(calls.map(([name]) => name)).not.toContain("drawImage");
  });

  test("a browser that gives no canvas to draw on rejects the same way", async () => {
    context = null;

    await expect(loadLogo("/logo.png")).rejects.toThrow("no canvas");
  });
});
