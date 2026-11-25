import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import type { ActionState } from "@/lib/api/server";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The real editor shell, which keeps the text it was built from; only the
// ProseMirror inside it is stubbed, as markdown-editor.test.tsx does, with a
// handle for an edit that reaches the shell through the editor's own
// listener rather than a DOM event.
const { editing } = vi.hoisted(() => {
  const editing: { emit: (markdown: string) => void; built: number; typed?: string } = {
    emit: () => {},
    built: 0,
  };
  return { editing };
});
vi.mock("@milkdown/crepe", () => ({
  Crepe: class {
    static Feature = { ImageBlock: "image-block", Latex: "latex", AI: "ai", BlockEdit: "block-edit", CodeMirror: "code-mirror" };
    editor = { use: () => this.editor };
    loaded: string;
    constructor({ defaultValue }: { defaultValue: string }) {
      this.loaded = defaultValue;
    }
    getMarkdown() {
      return editing.typed ?? this.loaded;
    }
    on(listen: (api: { markdownUpdated: (callback: (ctx: unknown, markdown: string) => void) => void }) => void) {
      listen({
        markdownUpdated: (callback) =>
          (editing.emit = (markdown) => {
            editing.typed ??= markdown;
            callback({}, markdown);
          }),
      });
      return this;
    }
    create() {
      editing.built += 1;
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

vi.mock("@/components/ui/paste-as-markdown", () => import("@/components/ui/markdown-editor-test-support"));

const { saveAction } = vi.hoisted(() => ({
  saveAction: vi.fn(async (previous: unknown, form: FormData): Promise<ActionState> => {
    void previous;
    void form;
    return { ok: true };
  }),
}));

import { PerLanguageForm } from "./per-language-form";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

beforeEach(() => {
  saveAction.mockClear();
  editing.built = 0;
  editing.typed = undefined;
});

function formAt(translations: Record<string, string>, version: number) {
  const t = dict.workspace.story;
  return (
    <PerLanguageForm
      contest={{ id: "f767af3b-f135-40d2-a3a6-82d368de1004", version }}
      name="body"
      languages={["en"]}
      defaultLanguage="en"
      translations={translations}
      editable
      action={saveAction}
      labels={{
        fallback: t.fallback,
        placeholder: t.placeholder,
        expand: t.expand,
        collapse: t.collapse,
        unavailable: t.unavailable,
        save: t.save,
        saving: t.saving,
        saved: t.saved,
        reload: dict.workspace.reload,
      }}
      errors={dict.errors}
    />
  );
}

/** What the form would send: its version and the box's text. */
function sent(container: HTMLElement) {
  const form = new FormData(container.querySelector("form")!);
  return { version: form.get("expectedVersion"), body: form.get("body.en") };
}

/** Waits for the editor to be built `times` times in all, and to have reported itself ready. */
async function built(times: number) {
  await waitFor(() => expect(editing.built).toBe(times));
  await act(async () => {});
}

describe("PerLanguageForm, a copy saved elsewhere", () => {
  // The editor keeps the text it was built from, so the form must build it
  // again: an old text at the fresh version would be saved over the newer one.
  test("an untouched form shows and sends the newer copy at its version", async () => {
    const view = render(formAt({ en: "Morning text." }, 3));
    await built(1);

    view.rerender(formAt({ en: "The colleague's text." }, 4));
    await built(2);

    expect(sent(view.container)).toEqual({ version: "4", body: "The colleague's text." });
    await userEvent.click(screen.getByRole("button", { name: dict.workspace.story.save }));
    await waitFor(() => expect(saveAction).toHaveBeenCalledTimes(1));
    expect(saveAction.mock.calls[0][1].get("body.en")).toBe("The colleague's text.");
    expect(saveAction.mock.calls[0][1].get("expectedVersion")).toBe("4");
  });

  // Enter, paste and undo reach the editor through its own handlers, with no
  // input event on the form: the box's report of its text is what pins it.
  test("an edit the editor reports without a DOM event keeps the text and the version it was based on", async () => {
    const view = render(formAt({ en: "Morning text." }, 3));
    await built(1);

    act(() => editing.emit("Morning text. Ours."));
    view.rerender(formAt({ en: "The colleague's text." }, 4));
    await act(async () => {});

    expect(sent(view.container)).toEqual({ version: "3", body: "Morning text. Ours." });
  });

  // Saved inside the editor's report delay (MarkdownEditor): the text the
  // editor holds is sent, and it counts as an edit, so the draft keeps the
  // version it was based on when the page re-reads after a refusal.
  test("an edit saved before the editor reported it is sent, and counts as an edit", async () => {
    saveAction.mockResolvedValueOnce({ code: "invalid_request" });
    const view = render(formAt({ en: "Morning text." }, 3));
    await built(1);

    editing.typed = "Morning text. Typed just now.";
    await userEvent.click(screen.getByRole("button", { name: dict.workspace.story.save }));
    await waitFor(() => expect(saveAction).toHaveBeenCalledTimes(1));
    view.rerender(formAt({ en: "Morning text." }, 4));

    expect(saveAction.mock.calls[0][1].get("body.en")).toBe("Morning text. Typed just now.");
    expect(sent(view.container).version).toBe("3");
  });

  // The editor's own report of an edit arrives after a debounce, so after a
  // Save that already sent the edit. It is no new edit: the saved form follows
  // the page's re-reads again, as an untouched one does.
  test("the editor's late report of an edit the save already sent is no new edit", async () => {
    let answer: (state: ActionState) => void = () => {};
    saveAction.mockImplementationOnce(() => new Promise<ActionState>((resolve) => (answer = resolve)));
    const view = render(formAt({ en: "Morning text." }, 3));
    await built(1);

    editing.typed = "Morning text. Ours.";
    await userEvent.click(screen.getByRole("button", { name: dict.workspace.story.save }));
    act(() => editing.emit("Morning text. Ours."));
    await act(async () => answer({ ok: true, version: 4 }));
    view.rerender(formAt({ en: "Morning text. Ours." }, 4));
    await built(2);
    view.rerender(formAt({ en: "Morning text. Ours." }, 5));

    expect(sent(view.container).version).toBe("5");
  });
});
