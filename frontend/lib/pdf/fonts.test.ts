import { describe, expect, test, vi } from "vitest";

import { FAMILIES, FONT_FILES, loadFonts, type ReadFont } from "./fonts";

const bytes = (file: string) => new TextEncoder().encode(file);

describe("FONT_FILES", () => {
  test("is the seven faces the families name, each once", () => {
    expect([...FONT_FILES].sort()).toEqual([
      "JetBrainsMono-Regular.ttf",
      "Literata-Italic.ttf",
      "Literata-Regular.ttf",
      "Literata-SemiBold.ttf",
      "Literata-SemiBoldItalic.ttf",
      "Onest-Medium.ttf",
      "Onest-Regular.ttf",
    ]);
    expect(new Set(Object.values(FAMILIES).flatMap((family) => Object.values(family)))).toEqual(new Set(FONT_FILES));
  });
});

describe("loadFonts", () => {
  test("reads every face from its source and hands back each one's bytes", async () => {
    const read = vi.fn<ReadFont>(async (_source, file) => bytes(file));

    const fonts = await loadFonts("every-face", read);

    expect([...fonts.keys()].sort()).toEqual([...FONT_FILES].sort());
    expect(fonts.get("Onest-Medium.ttf")).toEqual(bytes("Onest-Medium.ttf"));
    expect(read).toHaveBeenCalledWith("every-face", "Literata-Regular.ttf");
  });

  test("a load that succeeded is kept: asking again reads nothing", async () => {
    const read = vi.fn<ReadFont>(async (_source, file) => bytes(file));

    const first = await loadFonts("kept", read);
    read.mockClear();
    const second = await loadFonts("kept", read);

    expect(read).not.toHaveBeenCalled();
    expect(second).toBe(first);
  });

  test("a load that failed is forgotten: asking again reads every face again", async () => {
    let offline = true;
    const read = vi.fn<ReadFont>(async (_source, file) => {
      if (offline && file === "Onest-Medium.ttf") throw new Error("network down");
      return bytes(file);
    });

    await expect(loadFonts("forgotten", read)).rejects.toThrow("network down");
    offline = false;
    read.mockClear();
    const fonts = await loadFonts("forgotten", read);

    expect(read).toHaveBeenCalledTimes(FONT_FILES.length);
    expect(fonts.get("Onest-Medium.ttf")).toEqual(bytes("Onest-Medium.ttf"));
  });

  test("each source is loaded on its own", async () => {
    const read = vi.fn<ReadFont>(async (source, file) => bytes(`${source}/${file}`));

    const [one, two] = await Promise.all([loadFonts("one", read), loadFonts("two", read)]);

    expect(one.get("Literata-Regular.ttf")).toEqual(bytes("one/Literata-Regular.ttf"));
    expect(two.get("Literata-Regular.ttf")).toEqual(bytes("two/Literata-Regular.ttf"));
  });
});
