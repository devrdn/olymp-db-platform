import { describe, expect, test } from "vitest";

import { storyFileName } from "./file-name";

describe("storyFileName", () => {
  test("is the title, the language and .pdf", () => {
    expect(storyFileName("Murder in the Greenhouse", "en")).toBe("Murder-in-the-Greenhouse-en.pdf");
  });

  test("keeps Cyrillic and Romanian letters, which every current file system holds", () => {
    expect(storyFileName("Убийство в оранжерее", "ru")).toBe("Убийство-в-оранжерее-ru.pdf");
    expect(storyFileName("Crima din seră: Ștefan și țesătorul", "ro")).toBe("Crima-din-seră-Ștefan-și-țesătorul-ro.pdf");
  });

  test.each([
    ["a/b\\c", "a-b-c"],
    ['what? "who" <me> | *', "what-who-me"],
    ["tab\there\nnow", "tab-here-now"],
  ])("takes the characters a file system refuses out of %j", (title, expected) => {
    expect(storyFileName(title, "en")).toBe(`${expected}-en.pdf`);
  });

  test("never begins with a dot, ends with one, or leaves a path to climb out of", () => {
    expect(storyFileName("..", "en")).toBe("story-en.pdf");
    expect(storyFileName("../../etc/passwd", "en")).toBe("etc-passwd-en.pdf");
    expect(storyFileName("The end...", "en")).toBe("The-end-en.pdf");
  });

  test("names a story whose title is empty or only refused characters", () => {
    expect(storyFileName("", "ro")).toBe("story-ro.pdf");
    expect(storyFileName(' /\\:*?"<>| ', "ro")).toBe("story-ro.pdf");
  });

  test("avoids the names Windows keeps for devices", () => {
    expect(storyFileName("CON", "en")).toBe("_CON-en.pdf");
    expect(storyFileName("lpt1", "en")).toBe("_lpt1-en.pdf");
  });

  test("is bounded, and does not cut a letter in two", () => {
    const name = storyFileName("ș".repeat(500), "en");
    expect([...name.slice(0, -"-en.pdf".length)]).toHaveLength(80);
    expect(name.endsWith("-en.pdf")).toBe(true);
  });

  test("takes only plain characters from the language code", () => {
    expect(storyFileName("T", "../x")).toBe("T-x.pdf");
  });
});
