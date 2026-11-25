import { EditorView } from "@codemirror/view";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import type { Game } from "@/lib/api/game";
import { FALLBACK_MAX_GAME_SCRIPT_BYTES } from "@/lib/api/game-terms";

import { GameEditor } from "./game-editor";

const saved = vi.hoisted(() => ({ current: { ok: true } as { ok?: true; code?: string } }));
const status = vi.hoisted(() => ({ current: null as Game | null }));
const saveGameScriptAction = vi.hoisted(() =>
  vi.fn(async (_previous: unknown, form: FormData) => {
    void form;
    return saved.current;
  }),
);
const gameStatusAction = vi.hoisted(() => vi.fn(async () => status.current));

vi.mock("./actions", () => ({ saveGameScriptAction, gameStatusAction }));
const refresh = vi.hoisted(() => vi.fn());
vi.mock("next/navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("next/navigation")>()),
  useRouter: () => ({ refresh }),
}));

function game(overrides: Partial<Game> = {}): Game {
  return {
    status: "absent",
    version: 0,
    database: "",
    source: "editor",
    upload: undefined,
    buildError: "",
    buildErrorCode: "",
    scriptBytes: 0,
    maxScriptBytes: 512 * 1024,
    building: false,
    updatedAt: "",
    needsBuild: false,
    uploadLimits: { enabled: false, chunkBytes: 0, maxFileBytes: 0 },
    ...overrides,
  };
}

function editorAt(initial: Game, { editable = true, script = "", version = 3 } = {}) {
  return (
    <GameEditor
      contest={{ id: "11111111-1111-1111-1111-111111111111", version }}
      initial={initial}
      initialScript={script}
      editable={editable}
      dict={en}
    />
  );
}

function show(initial: Game, options: { editable?: boolean; script?: string } = {}) {
  return render(editorAt(initial, options));
}

beforeEach(() => {
  saved.current = { ok: true };
  status.current = null;
  gameStatusAction.mockClear();
});

afterEach(() => {
  vi.useRealTimers();
});

const t = en.workspace.game;

describe("the game editor", () => {
  test("says a contest has no game yet rather than looking empty", () => {
    show(game());

    expect(screen.getByText(t.status.absent)).toBeInTheDocument();
  });

  // "The build failed" alone gives the author nothing to act on.
  test("shows PostgreSQL's own words when the build failed", () => {
    show(game({ status: "failed", buildError: `ERROR: type "nosuchtype" does not exist` }));

    expect(screen.getByText(t.buildError)).toBeInTheDocument();
    expect(screen.getByText(/nosuchtype/)).toBeInTheDocument();
  });

  // PostgreSQL said nothing here: the platform stopped the build, so the
  // sentence is the interface's own, in the reader's language.
  test("says in its own words that the game grew too large for the installation", () => {
    show(
      game({
        status: "failed",
        buildError: "The game's database grew past the size one game may take on this installation.",
        buildErrorCode: "game_template_too_large",
      }),
    );

    expect(screen.getByText(t.buildError)).toBeInTheDocument();
    expect(screen.getByText(en.errors.game_template_too_large)).toBeInTheDocument();
    expect(screen.queryByText(t.buildErrorLede)).not.toBeInTheDocument();
  });

  // The editor is empty for a file-sourced game, so only this note says where
  // the game came from.
  test("says the active game was built from an uploaded file, not this editor", () => {
    show(game({ status: "ready", version: 1, source: "file" }));

    expect(screen.getByText(t.sourceFile)).toBeInTheDocument();
  });

  test("says nothing about a file for a game authored right here", () => {
    show(game({ status: "ready", version: 1, source: "editor" }));

    expect(screen.queryByText(t.sourceFile)).not.toBeInTheDocument();
  });

  // Likewise for a builder-sourced game.
  test("says the active game was described with the table builder, not this editor", () => {
    show(game({ status: "ready", version: 1, source: "builder" }));

    expect(screen.getByText(t.sourceBuilder)).toBeInTheDocument();
    expect(screen.queryByText(t.sourceFile)).not.toBeInTheDocument();
  });

  // Replacing a game makes every participant's copy stale and remade.
  test("cannot be saved once the contest has started", () => {
    show(game({ status: "ready", version: 1 }), { editable: false });

    expect(screen.getByRole("button", { name: t.save })).toBeDisabled();
    expect(screen.getByText(t.frozen)).toBeInTheDocument();
  });

  // The ceiling comes from the server's `maxScriptBytes`, never a bundled
  // constant (CLAUDE.md rule 11); an otherwise unused number proves it.
  test("refuses to submit a script past the size limit the server published", () => {
    // Opened with the script rather than typed: typing it was slow enough to
    // disturb another suite's timing.
    show(game({ maxScriptBytes: 200 }), { script: "x".repeat(201) });

    expect(screen.getByRole("button", { name: t.save })).toBeDisabled();
    expect(screen.getByText(t.tooLong)).toBeInTheDocument();
  });

  // Exactly at the ceiling is accepted.
  test("accepts a script of exactly the size limit the server published", () => {
    show(game({ maxScriptBytes: 200 }), { script: "x".repeat(200) });

    expect(screen.getByRole("button", { name: t.save })).not.toBeDisabled();
    expect(screen.queryByText(t.tooLong)).not.toBeInTheDocument();
  });

  // An older API sends zero; fall back to the built-in ceiling rather than
  // refusing nothing.
  test("falls back to the built-in ceiling when the server published none", () => {
    show(game({ maxScriptBytes: 0 }), { script: "x".repeat(FALLBACK_MAX_GAME_SCRIPT_BYTES + 1) });

    expect(screen.getByRole("button", { name: t.save })).toBeDisabled();
    expect(screen.getByText(t.tooLong)).toBeInTheDocument();
  });

  test("a saved script reports that the build has started", async () => {
    const user = userEvent.setup();
    show(game(), { script: "SELECT 1" });

    await user.click(screen.getByRole("button", { name: t.save }));

    await waitFor(() => expect(screen.getByText(t.saved)).toBeInTheDocument());
    // Shows the save's `pending` answer without waiting for a poll.
    expect(screen.getByText(t.status.pending)).toBeInTheDocument();
  });

  test("asks again while the build runs, and stops once it is not running", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    show(game({ status: "building", building: true, version: 1 }));

    status.current = game({ status: "ready", building: false, version: 1, database: "game_tpl_cabc" });
    await vi.advanceTimersByTimeAsync(2100);

    await waitFor(() => expect(screen.getByText(t.status.ready)).toBeInTheDocument());

    const asked = gameStatusAction.mock.calls.length;
    // Polling must stop once the build ends.
    await vi.advanceTimersByTimeAsync(6000);
    expect(gameStatusAction.mock.calls.length).toBe(asked);
  });

  test("a refusal is shown in the organiser's own language, not as a code", async () => {
    const user = userEvent.setup();
    saved.current = { code: "game_not_editable" };
    show(game(), { script: "SELECT 1" });

    await user.click(screen.getByRole("button", { name: t.save }));

    await waitFor(() => expect(screen.getByText(en.errors.game_not_editable)).toBeInTheDocument());
  });

  test("a refusal this build has no sentence for shows the fallback, never the code", async () => {
    const user = userEvent.setup();
    saved.current = { code: "game_code_added_later" };
    show(game(), { script: "SELECT 1" });

    await user.click(screen.getByRole("button", { name: t.save }));

    expect(await screen.findByRole("alert")).toHaveTextContent(en.errors.fallback);
    expect(screen.queryByText(/game_code_added_later/)).not.toBeInTheDocument();
  });

  // The stored code is the server's text, and the dictionary only inherits
  // `constructor`: the build's own words are shown, as for any unknown code.
  test("reads a build code the dictionary only inherits as unknown", () => {
    show(game({ status: "failed", buildError: "ERROR: boom", buildErrorCode: "constructor" }));

    expect(screen.getByText(t.buildErrorLede)).toBeInTheDocument();
    expect(screen.getByText("ERROR: boom")).toBeInTheDocument();
  });
});


describe("the game editor, the version it was rendered at", () => {
  test("a save over a contest that moved since says so, and the reload starts the form over", async () => {
    saved.current = { code: "contest_changed" };
    refresh.mockReset();
    show(game());

    await userEvent.click(screen.getByRole("button", { name: t.save }));
    expect(await screen.findByRole("alert")).toHaveTextContent(en.errors.contest_changed);
    await userEvent.click(screen.getByRole("button", { name: en.workspace.reload }));

    expect(refresh).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  // The save re-reads the page, whose contest now carries the moved version.
  test("a save sends the version it was rendered at, and the next one the version the page re-read", async () => {
    saveGameScriptAction.mockClear();
    const view = show(game());

    await userEvent.click(screen.getByRole("button", { name: t.save }));
    await waitFor(() => expect(saveGameScriptAction).toHaveBeenCalledTimes(1));
    view.rerender(editorAt(game(), { version: 4 }));
    await userEvent.click(screen.getByRole("button", { name: t.save }));

    await waitFor(() => expect(saveGameScriptAction).toHaveBeenCalledTimes(2));
    expect(saveGameScriptAction.mock.calls.map(([, form]) => form.get("expectedVersion"))).toEqual(["3", "4"]);
  });
});

describe("the game editor, a script saved elsewhere", () => {
  /** What the form would send: its version and the script. */
  function sent(container: HTMLElement) {
    const form = new FormData(container.querySelector("form")!);
    return { version: form.get("expectedVersion"), script: form.get("script") };
  }

  async function builtEditor(container: HTMLElement): Promise<EditorView> {
    await waitFor(() => expect(container.querySelector(".cm-editor")).toBeInTheDocument());
    return EditorView.findFromDOM(container.querySelector<HTMLElement>(".cm-editor")!)!;
  }

  // CodeMirror reads its text once, so the form must build it again: an old
  // script at the fresh version would be saved over the newer one.
  test("an untouched editor shows and sends the newer script at its version", async () => {
    const view = render(editorAt(game({ version: 1 }), { script: "CREATE TABLE morning (id int);" }));
    await builtEditor(view.container);

    view.rerender(editorAt(game({ version: 2 }), { script: "CREATE TABLE colleague (id int);", version: 4 }));
    const editor = await builtEditor(view.container);

    expect(editor.state.doc.toString()).toBe("CREATE TABLE colleague (id int);");
    expect(sent(view.container)).toEqual({ version: "4", script: "CREATE TABLE colleague (id int);" });
  });

  // Enter, paste and undo reach CodeMirror through its own handlers, with no
  // input event on the form: the editor's report of its text is what pins it.
  test("an edit the editor makes without a DOM event keeps the script and the version it was based on", async () => {
    const view = render(editorAt(game({ version: 1 }), { script: "CREATE TABLE morning (id int);" }));
    const editor = await builtEditor(view.container);

    act(() => editor.dispatch({ changes: { from: 0, insert: "-- ours\n" } }));
    view.rerender(editorAt(game({ version: 2 }), { script: "CREATE TABLE colleague (id int);", version: 4 }));

    expect(sent(view.container)).toEqual({ version: "3", script: "-- ours\nCREATE TABLE morning (id int);" });
  });
});
