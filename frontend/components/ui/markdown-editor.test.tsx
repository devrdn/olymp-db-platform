import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";

// The editor itself is a ProseMirror instance bound to a DOM node, and it does
// not survive jsdom. What is under test is the shell around it: the control
// that expands it, what that does to the page, and the field the form submits.
vi.mock("@milkdown/crepe", () => ({
  Crepe: class {
    static Feature = {
      ImageBlock: "image-block",
      Latex: "latex",
      AI: "ai",
      BlockEdit: "block-edit",
    };
    // The Milkdown editor underneath, which the component adds its paste
    // handler to before creating anything.
    editor = { use: () => this.editor };
    on() {
      return this;
    }
    create() {
      return Promise.resolve();
    }
    destroy() {
      return Promise.resolve();
    }
    setReadonly() {
      return this;
    }
  },
}));

import { MarkdownEditor } from "./markdown-editor";

const labels = { expand: "Open full screen", collapse: "Close full screen", unavailable: "нет" };

afterEach(() => {
  document.body.style.overflow = "";
});

describe("MarkdownEditor, the field it submits", () => {
  test("carries the document under the name the form expects", () => {
    const { container } = render(
      <MarkdownEditor name="body.en" defaultValue="A body in the stacks." labels={labels} />,
    );

    const field = container.querySelector('input[name="body.en"]');
    expect(field).toHaveValue("A body in the stacks.");
  });
});

describe("MarkdownEditor, full screen", () => {
  test("opens and closes from one control that says which it will do", async () => {
    // Two labels on one button, never two buttons: a page carrying both would
    // have one that is always wrong.
    render(<MarkdownEditor name="body.en" defaultValue="" labels={labels} />);

    const toggle = screen.getByRole("button", { name: labels.expand });
    expect(toggle).toHaveAttribute("aria-expanded", "false");

    await userEvent.click(toggle);
    expect(screen.getByRole("button", { name: labels.collapse })).toHaveAttribute(
      "aria-expanded",
      "true",
    );

    await userEvent.click(screen.getByRole("button", { name: labels.collapse }));
    expect(screen.getByRole("button", { name: labels.expand })).toBeInTheDocument();
  });

  test("stops the page behind it from scrolling while it is open", async () => {
    // Otherwise the wheel scrolls the page under a surface that fills the
    // screen, and closing it leaves the author somewhere they never went.
    render(<MarkdownEditor name="body.en" defaultValue="" labels={labels} />);

    await userEvent.click(screen.getByRole("button", { name: labels.expand }));
    expect(document.body.style.overflow).toBe("hidden");

    await userEvent.click(screen.getByRole("button", { name: labels.collapse }));
    expect(document.body.style.overflow).toBe("");
  });

  test("closes on Escape, which is what a full-screen surface owes the keyboard", async () => {
    render(<MarkdownEditor name="body.en" defaultValue="" labels={labels} />);
    await userEvent.click(screen.getByRole("button", { name: labels.expand }));

    await userEvent.keyboard("{Escape}");

    expect(screen.getByRole("button", { name: labels.expand })).toBeInTheDocument();
  });

  test("leaves nothing behind when it unmounts while open", async () => {
    // Navigating away from an expanded editor must not leave the page unable
    // to scroll, with no control left to fix it.
    const { unmount } = render(
      <MarkdownEditor name="body.en" defaultValue="" labels={labels} />,
    );
    await userEvent.click(screen.getByRole("button", { name: labels.expand }));
    // Guards against the assertion below passing because nothing ever opened.
    expect(document.body.style.overflow).toBe("hidden");

    unmount();

    expect(document.body.style.overflow).toBe("");
  });

  test("keeps the same editor rather than building a second one", async () => {
    // The editor is a ProseMirror instance bound to one node. Rendering a
    // separate copy for the expanded state would lose the undo history and,
    // on the way back, whatever was typed into the one being discarded.
    const { container } = render(
      <MarkdownEditor name="body.en" defaultValue="" labels={labels} />,
    );
    const before = container.querySelector("[data-editor-host]");

    await userEvent.click(screen.getByRole("button", { name: labels.expand }));

    expect(container.querySelector("[data-editor-host]")).toBe(before);
  });
});
