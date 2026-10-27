import { describe, expect, it } from "vitest";

import { cleanEditorMarkdown, safeUrl, storedMarkdown } from "./markdown";

describe("cleanEditorMarkdown", () => {
  it("drops a paragraph that is only a line break", () => {
    expect(cleanEditorMarkdown("# A title\n\n<br />\n\nA paragraph.")).toBe("# A title\n\nA paragraph.");
  });

  it("drops it however the editor spelled it", () => {
    for (const spelling of ["<br>", "<br/>", "<br />", "<BR />", "  <br />  "]) {
      expect(cleanEditorMarkdown(`A.\n\n${spelling}\n\nB.`)).toBe("A.\n\nB.");
    }
  });

  it("empties a table cell that holds nothing but a line break", () => {
    const table = "| a | b |\n| :- | :- |\n| <br /> | x |";
    expect(cleanEditorMarkdown(table)).toBe("| a | b |\n| :- | :- |\n|  | x |");
  });

  it("never touches a fenced code block", () => {
    const story = "Before.\n\n```html\n<br />\n```\n\nAfter.";
    expect(cleanEditorMarkdown(story)).toBe(story);
  });

  it("never touches a fence that uses tildes", () => {
    const story = "Before.\n\n~~~\n<br />\n~~~\n\nAfter.";
    expect(cleanEditorMarkdown(story)).toBe(story);
  });

  it("keeps a line break an author used inside a line", () => {
    const written = "First line<br />second line.";
    expect(cleanEditorMarkdown(written)).toBe(written);
  });

  it("leaves ordinary prose exactly as it was", () => {
    const story = "# The Greenhouse Case\n\nA body in the stacks.\n\n- one\n- two\n";
    expect(cleanEditorMarkdown(story)).toBe(story);
  });

  it("collapses the run of blank lines a removed paragraph leaves behind", () => {
    expect(cleanEditorMarkdown("A.\n\n<br />\n\n<br />\n\nB.")).toBe("A.\n\nB.");
  });

  it("survives an empty document", () => {
    expect(cleanEditorMarkdown("")).toBe("");
  });
});

describe("safeUrl", () => {
  it.each(["https://example.org/a?b=c", "http://example.org", "mailto:a@example.org", "/relative/path", "#anchor"])(
    "lets %s through",
    (url) => expect(safeUrl(url)).toBe(url),
  );

  it.each(["javascript:alert(1)", "JaVaScRiPt:alert(1)", "data:text/html,<b>x</b>", "vbscript:x", "file:///etc/passwd"])(
    "refuses %s",
    (url) => expect(safeUrl(url)).toBe(""),
  );
});

describe("storedMarkdown", () => {
  it("drops the final newline the editor's serializer appends", () => {
    expect(storedMarkdown("A paragraph.\n")).toBe("A paragraph.");
  });

  it("drops the empty paragraphs the editor writes as a break", () => {
    expect(storedMarkdown("One.\n\n<br />\n\nTwo.\n")).toBe("One.\n\nTwo.");
  });

  it("is the same for what was saved and what the editor makes of it again", () => {
    const saved = "# Title\n\n- one\n- two";
    expect(storedMarkdown(`${saved}\n`)).toBe(storedMarkdown(saved));
  });

  it("is empty for a document with nothing in it", () => {
    expect(storedMarkdown("\n<br />\n")).toBe("");
  });
});
