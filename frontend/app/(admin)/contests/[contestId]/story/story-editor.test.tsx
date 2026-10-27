import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The real Server Action would pull in Next's server runtime; `vi.hoisted`
// because `vi.mock` factories run first.
const { saveStoryAction, serializer, pendingReady } = vi.hoisted(() => ({
  // How the stand-in editor writes Markdown back. Milkdown's serializer ends a
  // document with a newline, may rewrite what it loaded in its own form
  // (`*em*` as `_em_`, a `-` bullet as `*`), and may report that as a change.
  serializer: { suffix: "", normalise: false, changeOnLoad: false, readyLater: false },
  // With `readyLater`, the editor reports that it is built only when a test calls these.
  pendingReady: [] as (() => void)[],

  saveStoryAction: vi.fn(async (previous: unknown, form: FormData): Promise<{ code?: string; saved?: boolean }> => {
    void previous;
    void form;
    return { saved: true };
  }),
}));
vi.mock("./actions", () => ({ saveStoryAction }));

// ProseMirror does not survive jsdom; a plain field under the same name stands
// in for the editor, which has tests of its own.
vi.mock("@/components/ui/markdown-editor", async () => {
  const { useEffect, useRef } = await import("react");
  const write = (markdown: string) =>
    (serializer.normalise ? markdown.replace(/\*([^*\s][^*]*)\*/g, "_$1_").replace(/^- /gm, "* ") : markdown) +
    serializer.suffix;

  return {
    MarkdownEditor: ({
      name,
      defaultValue,
      placeholder,
      onChange,
      onReady,
    }: {
      name: string;
      defaultValue: string;
      placeholder?: string;
      onChange?: (markdown: string) => void;
      onReady?: (markdown: string, loaded: string) => void;
    }) => {
      // As the real editor does, it calls the latest callback it was given, not the first.
      const latest = useRef(onReady);
      useEffect(() => {
        latest.current = onReady;
      });
      useEffect(() => {
        const ready = () => latest.current?.(write(defaultValue), defaultValue);
        if (serializer.readyLater) pendingReady.push(ready);
        else ready();
        if (serializer.changeOnLoad) onChange?.(write(defaultValue));
        // Once, as the editor reports it once it is built.
        // eslint-disable-next-line react-hooks/exhaustive-deps
      }, []);
      return (
        <textarea
          name={name}
          defaultValue={defaultValue}
          placeholder={placeholder}
          aria-label={name}
          onChange={(event) => onChange?.(write(event.currentTarget.value))}
        />
      );
    },
  };
});

import { StoryEditor } from "./story-editor";

const contestId = "f767af3b-f135-40d2-a3a6-82d368de1004";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

beforeEach(() => {
  saveStoryAction.mockClear();
  Object.assign(serializer, { suffix: "", normalise: false, changeOnLoad: false, readyLater: false });
  pendingReady.length = 0;
});

function editorFor(translations: Record<string, string>, editable = true) {
  return (
    <StoryEditor
      contestId={contestId}
      languages={["en", "ro"]}
      defaultLanguage="en"
      translations={translations}
      editable={editable}
      dict={dict}
      pdf={{ titles: { en: "The Greenhouse", ro: "Sera" }, logoHref: "/api/v1/settings/images/logo?v=abc" }}
    />
  );
}

function show(overrides: { editable?: boolean; translations?: Record<string, string> } = {}) {
  return render(editorFor(overrides.translations ?? { en: "A body in the stacks." }, overrides.editable ?? true));
}

describe("StoryEditor", () => {
  test("offers one box per language with what is written, the default language marked", () => {
    show();

    expect(screen.getByRole("textbox", { name: "body.en" })).toHaveValue("A body in the stacks.");
    expect(screen.getByRole("textbox", { name: "body.ro" })).toHaveValue("");
    expect(screen.getByRole("textbox", { name: "body.en" })).toHaveAttribute("placeholder", dict.workspace.story.placeholder);
    expect(screen.getAllByText(dict.workspace.story.fallback)).toHaveLength(1);
  });

  test("sends every box at once and says it saved", async () => {
    show();

    await userEvent.type(screen.getByRole("textbox", { name: "body.ro" }), "Un cadavru.");
    await userEvent.click(screen.getByRole("button", { name: dict.workspace.story.save }));

    await waitFor(() => expect(saveStoryAction).toHaveBeenCalledTimes(1));
    const form = saveStoryAction.mock.calls[0][1];
    expect(form.get("contestId")).toBe(contestId);
    expect(form.get("body.en")).toBe("A body in the stacks.");
    expect(form.get("body.ro")).toBe("Un cadavru.");
    expect(await screen.findByRole("status")).toHaveTextContent(dict.workspace.story.saved);
  });

  test("says why a save was refused, in the reader's language", async () => {
    saveStoryAction.mockResolvedValueOnce({ code: "not_editable" });
    show();

    await userEvent.click(screen.getByRole("button", { name: dict.workspace.story.save }));

    expect(await screen.findByRole("alert")).toHaveTextContent(dict.errors.not_editable);
  });

  test("once frozen, shows the story as participants see it, with nothing to edit", () => {
    show({ editable: false, translations: { en: "## The greenhouse\n\n<b>raw</b>" } });

    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "The greenhouse" })).toBeInTheDocument();
    expect(screen.getByText(/<b>raw<\/b>/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: dict.workspace.story.save })).toBeDisabled();
  });

  describe("the PDF", () => {
    const t = () => dict.workspace.story;
    const pdfButton = (lang: string) => screen.getByRole("button", { name: `${t().pdf}, ${lang}` });

    test("each language has its own button, available only where there is saved text", () => {
      show();

      expect(pdfButton("en")).toBeEnabled();
      expect(pdfButton("ro")).toBeDisabled();
      expect(screen.getByText(t().pdfEmpty)).toBeInTheDocument();
    });

    test("typing in one language disables that language's button until it is saved, and no other", async () => {
      show({ translations: { en: "A body in the stacks.", ro: "Un cadavru." } });

      await userEvent.type(screen.getByRole("textbox", { name: "body.en" }), " More.");

      expect(pdfButton("en")).toBeDisabled();
      expect(screen.getByText(t().pdfUnsaved)).toBeInTheDocument();
      expect(pdfButton("ro")).toBeEnabled();
    });

    test("putting the saved text back makes the file available again", async () => {
      show();
      const box = screen.getByRole("textbox", { name: "body.en" });

      await userEvent.type(box, "!");
      expect(pdfButton("en")).toBeDisabled();

      await userEvent.type(box, "{Backspace}");
      expect(pdfButton("en")).toBeEnabled();
    });

    test("an editor that ends its text with a newline, as Milkdown's does, still compares equal to what is saved", async () => {
      serializer.suffix = "\n";
      show();
      const box = screen.getByRole("textbox", { name: "body.en" });

      await userEvent.type(box, "!");
      expect(pdfButton("en")).toBeDisabled();

      await userEvent.type(box, "{Backspace}");
      expect(pdfButton("en")).toBeEnabled();
    });

    test("an empty paragraph the editor writes as a break is not a change either", async () => {
      serializer.suffix = "\n\n<br />\n";
      show();
      await userEvent.type(screen.getByRole("textbox", { name: "body.en" }), "{Backspace}!");

      expect(pdfButton("en")).toBeDisabled();
      await userEvent.type(screen.getByRole("textbox", { name: "body.en" }), "{Backspace}.");
      expect(pdfButton("en")).toBeEnabled();
    });

    test("text the editor writes in its own form is not a change: the file is available on load and after an edit is undone", async () => {
      Object.assign(serializer, { suffix: "\n", normalise: true, changeOnLoad: true });
      show({ translations: { en: "The *gardener* lied.\n\n- one\n- two" } });

      expect(pdfButton("en")).toBeEnabled();

      const box = screen.getByRole("textbox", { name: "body.en" });
      await userEvent.type(box, "!");
      expect(pdfButton("en")).toBeDisabled();
      await userEvent.type(box, "{Backspace}");
      expect(pdfButton("en")).toBeEnabled();
    });

    test("once the edit is saved, the new saved text is what the editor is compared with", async () => {
      Object.assign(serializer, { suffix: "\n", normalise: true });
      const view = show({ translations: { en: "The *gardener* lied." } });

      await userEvent.type(screen.getByRole("textbox", { name: "body.en" }), "!");
      expect(pdfButton("en")).toBeDisabled();

      // The page reloads the story after a save: what the editor held, as the save stores it.
      view.rerender(editorFor({ en: "The _gardener_ lied.!" }));

      expect(pdfButton("en")).toBeEnabled();
    });

    test("an edit left unsaved when the story freezes no longer holds the file back, nor after it thaws", async () => {
      const view = show();
      await userEvent.type(screen.getByRole("textbox", { name: "body.en" }), "!");
      expect(pdfButton("en")).toBeDisabled();

      // The contest started: the page shows the saved text, and there is nothing left to save.
      view.rerender(editorFor({ en: "A body in the stacks." }, false));
      expect(pdfButton("en")).toBeEnabled();
      expect(screen.queryByText(t().pdfUnsaved)).not.toBeInTheDocument();

      // Editable again: a fresh editor holding the saved text, until it is edited.
      view.rerender(editorFor({ en: "A body in the stacks." }, true));
      expect(pdfButton("en")).toBeEnabled();
      await userEvent.type(screen.getByRole("textbox", { name: "body.en" }), "?");
      expect(pdfButton("en")).toBeDisabled();
    });

    test("the editor's writing is paired with the text it loaded, even when the saved text changed before it was ready", async () => {
      Object.assign(serializer, { suffix: "\n", normalise: true, readyLater: true });
      const view = show({ translations: { en: "The *gardener* lied." } });

      // Saved elsewhere while this editor was still being built; it still holds the old text.
      view.rerender(editorFor({ en: "Someone else wrote this." }));
      act(() => pendingReady.forEach((ready) => ready()));

      const box = screen.getByRole("textbox", { name: "body.en" });
      await userEvent.type(box, "!");
      await userEvent.type(box, "{Backspace}");

      // The editor holds the old text, which is not what is saved now.
      expect(pdfButton("en")).toBeDisabled();
    });

    test("once frozen, the story can still be downloaded", () => {
      show({ editable: false, translations: { en: "## The greenhouse" } });

      expect(pdfButton("en")).toBeEnabled();
    });
  });
});
