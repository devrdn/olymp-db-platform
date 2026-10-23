import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The real Server Action would pull in Next's server runtime; `vi.hoisted`
// because `vi.mock` factories run first.
const { saveStoryAction } = vi.hoisted(() => ({
  saveStoryAction: vi.fn(async (previous: unknown, form: FormData): Promise<{ code?: string; saved?: boolean }> => {
    void previous;
    void form;
    return { saved: true };
  }),
}));
vi.mock("./actions", () => ({ saveStoryAction }));

// ProseMirror does not survive jsdom; a plain field under the same name stands
// in for the editor, which has tests of its own.
vi.mock("@/components/ui/markdown-editor", () => ({
  MarkdownEditor: ({ name, defaultValue, placeholder }: { name: string; defaultValue: string; placeholder?: string }) => (
    <textarea name={name} defaultValue={defaultValue} placeholder={placeholder} aria-label={name} />
  ),
}));

import { StoryEditor } from "./story-editor";

const contestId = "f767af3b-f135-40d2-a3a6-82d368de1004";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

beforeEach(() => {
  saveStoryAction.mockClear();
});

function show(overrides: { editable?: boolean; translations?: Record<string, string> } = {}) {
  return render(
    <StoryEditor
      contestId={contestId}
      languages={["en", "ro"]}
      defaultLanguage="en"
      translations={overrides.translations ?? { en: "A body in the stacks." }}
      editable={overrides.editable ?? true}
      dict={dict}
    />,
  );
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
});
