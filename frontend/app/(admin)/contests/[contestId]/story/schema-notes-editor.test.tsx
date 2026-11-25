import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The real Server Action would pull in Next's server runtime; `vi.hoisted`
// because `vi.mock` factories run first.
const { saveSchemaNotesAction } = vi.hoisted(() => ({
  saveSchemaNotesAction: vi.fn(async (previous: unknown, form: FormData): Promise<{ code?: string; ok?: true }> => {
    void previous;
    void form;
    return { ok: true };
  }),
}));
vi.mock("./actions", () => ({ saveSchemaNotesAction }));
const { refresh } = vi.hoisted(() => ({ refresh: vi.fn() }));
vi.mock("next/navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("next/navigation")>()),
  useRouter: () => ({ refresh }),
}));

// ProseMirror does not survive jsdom; a plain field under the same name stands
// in for the editor, which has tests of its own.
vi.mock("@/components/ui/markdown-editor", () => ({
  MarkdownEditor: ({ name, defaultValue, placeholder }: { name: string; defaultValue: string; placeholder?: string }) => (
    <textarea name={name} defaultValue={defaultValue} placeholder={placeholder} aria-label={name} />
  ),
}));

import { SchemaNotesEditor } from "./schema-notes-editor";

const contestId = "f767af3b-f135-40d2-a3a6-82d368de1004";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

beforeEach(() => {
  saveSchemaNotesAction.mockClear();
});

function editorFor(overrides: { editable?: boolean; translations?: Record<string, string>; version?: number } = {}) {
  return (
    <SchemaNotesEditor
      contest={{ id: contestId, version: overrides.version ?? 3 }}
      languages={["en", "ro"]}
      defaultLanguage="ro"
      translations={overrides.translations ?? { ro: "Tabelul `suspects`." }}
      editable={overrides.editable ?? true}
      dict={dict}
    />
  );
}

function show(overrides: { editable?: boolean; translations?: Record<string, string> } = {}) {
  return render(editorFor(overrides));
}

describe("SchemaNotesEditor", () => {
  test("is its own section, saying what it is for and that hiding the catalogue does not hide it", () => {
    show();
    const t = dict.workspace.schemaNotes;

    expect(screen.getByRole("heading", { name: t.heading })).toBeInTheDocument();
    expect(screen.getByText(t.lede)).toBeInTheDocument();
    expect(screen.getByText(t.catalogueHint)).toBeInTheDocument();
  });

  test("offers one box per language with what is written, the default language marked", () => {
    show();

    expect(screen.getByRole("textbox", { name: "schemaNotes.ro" })).toHaveValue("Tabelul `suspects`.");
    expect(screen.getByRole("textbox", { name: "schemaNotes.en" })).toHaveValue("");
    expect(screen.getAllByText(dict.workspace.story.fallback)).toHaveLength(1);
  });

  test("sends every box at once and says it saved", async () => {
    show();

    await userEvent.type(screen.getByRole("textbox", { name: "schemaNotes.en" }), "The suspects table.");
    await userEvent.click(screen.getByRole("button", { name: dict.workspace.schemaNotes.save }));

    await waitFor(() => expect(saveSchemaNotesAction).toHaveBeenCalledTimes(1));
    const form = saveSchemaNotesAction.mock.calls[0][1];
    expect(form.get("contestId")).toBe(contestId);
    expect(form.get("schemaNotes.en")).toBe("The suspects table.");
    expect(form.get("schemaNotes.ro")).toBe("Tabelul `suspects`.");
    expect(await screen.findByRole("status")).toHaveTextContent(dict.workspace.schemaNotes.saved);
  });

  test("says why a save was refused, in the reader's language", async () => {
    saveSchemaNotesAction.mockResolvedValueOnce({ code: "not_editable" });
    show();

    await userEvent.click(screen.getByRole("button", { name: dict.workspace.schemaNotes.save }));

    expect(await screen.findByRole("alert")).toHaveTextContent(dict.errors.not_editable);
  });

  test("once frozen, shows the descriptions as participants see them, with nothing to edit", () => {
    show({ editable: false, translations: { ro: "## suspects\n\n<b>raw</b>" } });

    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "suspects" })).toBeInTheDocument();
    // StoryText: raw HTML stays text.
    expect(screen.getByText(/<b>raw<\/b>/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: dict.workspace.schemaNotes.save })).toBeDisabled();
  });

  test("once frozen, says so beside its heading and names a language left empty", () => {
    show({ editable: false, translations: { ro: "Tabelul `suspects`." } });

    const heading = screen.getByRole("heading", { name: dict.workspace.schemaNotes.heading });
    expect(heading.parentElement).toHaveTextContent(dict.workspace.facts.frozen);
    expect(screen.getAllByText(dict.workspace.schemaNotes.empty)).toHaveLength(1);
  });

  test("while editable, carries no frozen tag", () => {
    show();

    expect(screen.queryByText(dict.workspace.facts.frozen)).not.toBeInTheDocument();
    expect(screen.queryByText(dict.workspace.schemaNotes.empty)).not.toBeInTheDocument();
  });

  test("renders a long document whole once frozen", () => {
    const long = Array.from({ length: 200 }, (_, i) => `## table_${i}\n\nColumn \`id\` of table ${i}.`).join("\n\n");
    show({ editable: false, translations: { en: long } });

    expect(screen.getAllByRole("heading", { level: 2, name: /^table_\d+$/ })).toHaveLength(200);
  });
});

describe("SchemaNotesEditor, the version it was rendered at", () => {
  test("a save over a contest that moved since says so, and the reload starts the form over", async () => {
    saveSchemaNotesAction.mockResolvedValueOnce({ code: "contest_changed" });
    refresh.mockReset();
    show();

    await userEvent.click(screen.getByRole("button", { name: dict.workspace.schemaNotes.save }));
    expect(await screen.findByRole("alert")).toHaveTextContent(dict.errors.contest_changed);
    await userEvent.click(screen.getByRole("button", { name: dict.workspace.reload }));

    expect(refresh).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  // The save re-reads the page, whose contest now carries the moved version.
  test("a save sends the version it was rendered at, and the next one the version the page re-read", async () => {
    const view = show();

    await userEvent.click(screen.getByRole("button", { name: dict.workspace.schemaNotes.save }));
    expect(await screen.findByRole("status")).toHaveTextContent(dict.workspace.schemaNotes.saved);
    view.rerender(editorFor({ version: 4 }));
    await userEvent.click(screen.getByRole("button", { name: dict.workspace.schemaNotes.save }));

    await waitFor(() => expect(saveSchemaNotesAction).toHaveBeenCalledTimes(2));
    expect(saveSchemaNotesAction.mock.calls.map(([, form]) => form.get("expectedVersion"))).toEqual(["3", "4"]);
  });
});
