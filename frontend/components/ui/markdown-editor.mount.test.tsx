import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";

import { MarkdownEditor } from "./markdown-editor";

/**
 * The real editor, unmocked: the other tests stub Crepe and would pass with the
 * editor failing to start. jsdom has no layout, so visibility and clickability
 * need a browser.
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
    // The editor arrives on a dynamic import whose time depends on machine
    // load. `waitFor`'s default 1000ms failed with several `vitest run`
    // processes in parallel, so the ceiling is widened.
    const surface = await waitFor(
      () => {
        const found = document.querySelector(".ProseMirror");
        expect(found?.textContent).toContain("A heading");
        return found;
      },
      { timeout: 10_000 },
    );
    expect(surface).toHaveAttribute("contenteditable", "true");

    // The form field carries the Markdown that a save sends, not the rendered
    // text.
    const field = document.querySelector('input[name="story"]') as HTMLInputElement;
    expect(field.value).toContain("# A heading");
    // Above the `waitFor` budget, or Vitest's 5000ms default would cut the test
    // off first.
  }, 15_000);

  // The code block's editor is off; the fenced block itself must still survive
  // as Markdown.
  test("a fenced code block survives with its editor turned off", async () => {
    const story = "Before.\n\n```sql\nSELECT 1;\n```\n\nAfter.";
    render(
      <MarkdownEditor
        name="story"
        defaultValue={story}
        labels={{ expand: "e", collapse: "c", unavailable: "plain" }}
      />,
    );
    // A fresh editor instance again; same widened ceiling as above.
    await waitFor(
      () => expect(document.querySelector(".ProseMirror")?.textContent).toContain("SELECT 1;"),
      { timeout: 10_000 },
    );

    const field = document.querySelector('input[name="story"]') as HTMLInputElement;
    expect(field.value).toContain("```");
    expect(field.value).toContain("SELECT 1;");
  }, 15_000);
});

/**
 * The editor's bundle uses syntax (lookbehind, class static blocks) that older
 * browsers cannot parse, so it must be allowed not to arrive.
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

    // Exactly one field carries the name, or the form sends two values.
    expect(document.querySelectorAll('[name="story"]')).toHaveLength(1);

    vi.doUnmock("@milkdown/crepe");
  });
});
