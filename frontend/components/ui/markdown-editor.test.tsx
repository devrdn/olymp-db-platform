import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";

// ProseMirror does not survive jsdom; this tests the shell around it.
vi.mock("@milkdown/crepe", () => ({
  Crepe: class {
    static Feature = {
      ImageBlock: "image-block",
      Latex: "latex",
      AI: "ai",
      BlockEdit: "block-edit",
    };
    // The component adds its paste handler here before creating anything.
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
    // Two labels on one button, never two buttons.
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
    // Otherwise the page scrolls under the expanded editor.
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
    // Unmounting while expanded must release the scroll lock.
    const { unmount } = render(
      <MarkdownEditor name="body.en" defaultValue="" labels={labels} />,
    );
    await userEvent.click(screen.getByRole("button", { name: labels.expand }));
    // Guards against the assertion below passing because nothing opened.
    expect(document.body.style.overflow).toBe("hidden");

    unmount();

    expect(document.body.style.overflow).toBe("");
  });

  test("keeps the same editor rather than building a second one", async () => {
    // A second copy for the expanded state would lose the undo history.
    const { container } = render(
      <MarkdownEditor name="body.en" defaultValue="" labels={labels} />,
    );
    const before = container.querySelector("[data-editor-host]");

    await userEvent.click(screen.getByRole("button", { name: labels.expand }));

    expect(container.querySelector("[data-editor-host]")).toBe(before);
  });
});
