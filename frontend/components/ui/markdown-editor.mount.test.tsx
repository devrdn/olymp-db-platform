import { render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";

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
        labels={{ expand: "expand", collapse: "collapse", unavailable: "нет" }}
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

/**
 * The editor is allowed not to arrive.
 *
 * Its bundle carries a lookbehind and a class static block — syntax, not
 * behaviour — so a browser that lacks either throws while parsing and nothing
 * in the chunk runs. Neither can be transpiled away. Before this, the box
 * rendered, stayed empty, and its own controls did nothing: a silent failure
 * indistinguishable from a bug of ours, which is how it was reported.
 */
describe("when the editor cannot be loaded", () => {
  test("the story is still writable, and says why it looks plain", async () => {
    vi.doMock("@milkdown/crepe", () => {
      throw new SyntaxError("Unexpected token");
    });
    vi.resetModules();
    const { MarkdownEditor: Fresh } = await import("./markdown-editor");

    render(
      <Fresh
        name="story"
        defaultValue={"# A heading"}
        labels={{ expand: "e", collapse: "c", unavailable: "plain Markdown here" }}
      />,
    );
    await new Promise((resolve) => setTimeout(resolve, 300));

    const field = screen.getByRole("textbox") as HTMLTextAreaElement;
    expect(field).toHaveAttribute("name", "story");
    expect(field.value).toBe("# A heading");
    expect(screen.getByRole("status")).toHaveTextContent("plain Markdown here");

    // And exactly one field carries the name, or the form sends two values
    // for it.
    expect(document.querySelectorAll('[name="story"]')).toHaveLength(1);

    vi.doUnmock("@milkdown/crepe");
  });
});
