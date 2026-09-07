import { render, screen, waitFor } from "@testing-library/react";
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
    // Waited for, not slept through. The editor arrives on a dynamic import,
    // and how long that takes is a property of the machine running the suite
    // rather than of the editor: a fixed 800ms passed alone and failed under a
    // full-suite run, which is a flaky test rather than a caught bug.
    const surface = await waitFor(() => {
      const found = document.querySelector(".ProseMirror");
      expect(found?.textContent).toContain("A heading");
      return found;
    });
    expect(surface).toHaveAttribute("contenteditable", "true");

    // The form field carries the Markdown, not the rendered text: the field is
    // what a save actually sends.
    const field = document.querySelector('input[name="story"]') as HTMLInputElement;
    expect(field.value).toContain("# A heading");
  });

  // The code block's own editor is off, because it crashes. What must survive
  // that is the block itself: a fence is still written, saved and rendered as
  // Markdown, and only the syntax highlighting while editing is gone.
  test("a fenced code block survives with its editor turned off", async () => {
    const story = "Before.\n\n```sql\nSELECT 1;\n```\n\nAfter.";
    render(
      <MarkdownEditor
        name="story"
        defaultValue={story}
        labels={{ expand: "e", collapse: "c", unavailable: "plain" }}
      />,
    );
    await waitFor(() =>
      expect(document.querySelector(".ProseMirror")?.textContent).toContain("SELECT 1;"),
    );

    const field = document.querySelector('input[name="story"]') as HTMLInputElement;
    expect(field.value).toContain("```");
    expect(field.value).toContain("SELECT 1;");
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
    const field = (await screen.findByRole("textbox")) as HTMLTextAreaElement;
    expect(field).toHaveAttribute("name", "story");
    expect(field.value).toBe("# A heading");
    expect(screen.getByRole("status")).toHaveTextContent("plain Markdown here");

    // And exactly one field carries the name, or the form sends two values
    // for it.
    expect(document.querySelectorAll('[name="story"]')).toHaveLength(1);

    vi.doUnmock("@milkdown/crepe");
  });
});
