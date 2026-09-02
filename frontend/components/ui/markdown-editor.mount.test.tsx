import { render } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import { MarkdownEditor } from "./markdown-editor";

/**
 * The editor, unmocked.
 *
 * Its other tests replace Crepe with a stub, which is right for what they
 * check — the wrapper's own props and its one toggle — and wrong as evidence
 * that the editor works. Everything they assert would keep passing with the
 * real editor failing to start, and for a while that was the state of things
 * nobody could see from a green suite.
 *
 * What this cannot check is layout: jsdom has no boxes, so an editable surface
 * that is invisible or unclickable looks identical here to one that is not.
 * That has to be looked at in a browser, and was.
 */
describe("the real editor", () => {
  test("mounts and takes the document it was given", async () => {
    render(
      <MarkdownEditor
        name="story"
        defaultValue={"# A heading\n\nA paragraph."}
        labels={{ expand: "expand", collapse: "collapse" }}
      />,
    );
    await new Promise((resolve) => setTimeout(resolve, 800));

    const surface = document.querySelector(".ProseMirror");
    expect(surface).not.toBeNull();
    expect(surface).toHaveAttribute("contenteditable", "true");
    expect(surface?.textContent).toContain("A heading");

    // The form field carries the Markdown, not the rendered text: the field is
    // what a save actually sends.
    const field = document.querySelector('input[name="story"]') as HTMLInputElement;
    expect(field.value).toContain("# A heading");
  });
});
