import { describe, expect, it } from "vitest";

import { cleanEditorMarkdown } from "./markdown";

describe("cleanEditorMarkdown", () => {
  // What the WYSIWYG editor actually stores for an empty paragraph, and what
  // a reader sees when it does: the four characters, as text.
  it("drops a paragraph that is only a line break", () => {
    expect(cleanEditorMarkdown("# A title\n\n<br />\n\nA paragraph.")).toBe("# A title\n\nA paragraph.");
  });

  it("drops it however the editor spelled it", () => {
    for (const spelling of ["<br>", "<br/>", "<br />", "<BR />", "  <br />  "]) {
      expect(cleanEditorMarkdown(`A.\n\n${spelling}\n\nB.`)).toBe("A.\n\nB.");
    }
  });

  // An empty table cell is stored the same way, and `| <br /> |` is a table
  // with the tag printed in it.
  it("empties a table cell that holds nothing but a line break", () => {
    const table = "| a | b |\n| :- | :- |\n| <br /> | x |";
    expect(cleanEditorMarkdown(table)).toBe("| a | b |\n| :- | :- |\n|  | x |");
  });

  // The one thing that must survive untouched. A game's story shows SQL, and
  // a story about HTML shows HTML.
  it("never touches a fenced code block", () => {
    const story = "Before.\n\n```html\n<br />\n```\n\nAfter.";
    expect(cleanEditorMarkdown(story)).toBe(story);
  });

  it("never touches a fence that uses tildes", () => {
    const story = "Before.\n\n~~~\n<br />\n~~~\n\nAfter.";
    expect(cleanEditorMarkdown(story)).toBe(story);
  });

  // An author who wrote a break inside a sentence meant it. Only a break
  // standing alone as a paragraph is the editor's own artefact.
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
